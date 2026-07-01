package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/token"
)

func TestRepositoryCreateLookupAndQuota(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_one", "drop_one", "pick_one", now)

	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}

	got, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if got.ID != dp.ID || got.DropTokenHash != dp.DropTokenHash || got.PickupTokenHash != dp.PickupTokenHash {
		t.Fatalf("loaded drop point mismatch: %+v", got)
	}

	count, err := repo.CountActiveDropPointsByAPITokenID(context.Background(), dp.APITokenID, now)
	if err != nil {
		t.Fatalf("CountActiveDropPointsByAPITokenID: %v", err)
	}
	if count != 1 {
		t.Fatalf("active count = %d, want 1", count)
	}

	count, err = repo.CountActiveDropPointsByAPITokenID(context.Background(), dp.APITokenID, now.Add(11*time.Minute))
	if err != nil {
		t.Fatalf("CountActiveDropPointsByAPITokenID after expiry: %v", err)
	}
	if count != 0 {
		t.Fatalf("active count after expiry = %d, want 0", count)
	}
}

func TestRepositoryDropTokenLookupAndReceivingAbort(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_drop", "drop_secret", "pick_secret", now)
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}

	open, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_secret"), now)
	if err != nil {
		t.Fatalf("FindOpenDropPointByDropTokenHash: %v", err)
	}
	if open.ID != dp.ID {
		t.Fatalf("open ID = %q, want %q", open.ID, dp.ID)
	}
	if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("wrong"), now); !errors.Is(err, droppoint.ErrDropTokenInvalid) {
		t.Fatalf("wrong drop token err = %v, want ErrDropTokenInvalid", err)
	}

	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now); !errors.Is(err, droppoint.ErrDropPointNotOpen) {
		t.Fatalf("second BeginReceivingDrop err = %v, want ErrDropPointNotOpen", err)
	}
	if err := repo.ResetReceivingDrop(context.Background(), dp.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("ResetReceivingDrop: %v", err)
	}
	again, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_secret"), now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("FindOpen after reset: %v", err)
	}
	if again.Status != droppoint.StatusOpen {
		t.Fatalf("status after reset = %q, want open", again.Status)
	}
}

func TestRepositoryPickupAuthorizationIsScopedToDropPoint(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp1 := testDropPoint(t, "dp_one", "drop_one", "pick_one", now)
	dp2 := testDropPoint(t, "dp_two", "drop_two", "pick_two", now)
	for _, dp := range []droppoint.DropPoint{dp1, dp2} {
		if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
			t.Fatalf("CreateDropPoint %s: %v", dp.ID, err)
		}
	}

	if _, err := repo.AuthorizePickupToken(context.Background(), dp1.ID, token.HashSecret("pick_one"), now); err != nil {
		t.Fatalf("AuthorizePickupToken own token: %v", err)
	}
	if _, err := repo.AuthorizePickupToken(context.Background(), dp1.ID, token.HashSecret("pick_two"), now); !errors.Is(err, droppoint.ErrPickupTokenInvalid) {
		t.Fatalf("cross pickup token err = %v, want ErrPickupTokenInvalid", err)
	}
}

func TestRepositoryCommitCloseExpireAndPickupTimestamp(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_ready", "drop_ready", "pick_ready", now)
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), dp.ID, droppoint.CommitDropResult{EnvelopePath: "drop-points/dp_ready/envelope.json", PayloadPath: "drop-points/dp_ready/payload.bin", EncryptedSize: 42}, now.Add(time.Second)); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}
	ready, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("Find ready: %v", err)
	}
	if ready.Status != droppoint.StatusReady || ready.DroppedAt == nil || ready.EncryptedSize != 42 {
		t.Fatalf("ready row mismatch: %+v", ready)
	}

	firstPickup := now.Add(2 * time.Second)
	if err := repo.MarkFirstPickedUp(context.Background(), dp.ID, firstPickup); err != nil {
		t.Fatalf("MarkFirstPickedUp: %v", err)
	}
	if err := repo.MarkFirstPickedUp(context.Background(), dp.ID, now.Add(3*time.Second)); err != nil {
		t.Fatalf("MarkFirstPickedUp again: %v", err)
	}
	picked, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("Find picked: %v", err)
	}
	if picked.FirstPickedUpAt == nil || !picked.FirstPickedUpAt.Equal(firstPickup) {
		t.Fatalf("first pickup = %v, want %v", picked.FirstPickedUpAt, firstPickup)
	}

	if err := repo.CloseDropPoint(context.Background(), dp.ID, now.Add(4*time.Second)); err != nil {
		t.Fatalf("CloseDropPoint: %v", err)
	}
	if err := repo.CloseDropPoint(context.Background(), dp.ID, now.Add(5*time.Second)); err != nil {
		t.Fatalf("CloseDropPoint retry: %v", err)
	}
	closed, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("Find closed: %v", err)
	}
	if closed.Status != droppoint.StatusClosed || closed.ClosedAt == nil {
		t.Fatalf("closed row mismatch: %+v", closed)
	}
}

func TestRepositoryExpireDropPoints(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	expired := testDropPoint(t, "dp_expired", "drop_expired", "pick_expired", now.Add(-20*time.Minute))
	active := testDropPoint(t, "dp_active", "drop_active", "pick_active", now)
	for _, dp := range []droppoint.DropPoint{expired, active} {
		if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
			t.Fatalf("CreateDropPoint %s: %v", dp.ID, err)
		}
	}

	affected, err := repo.ExpireDropPoints(context.Background(), now)
	if err != nil {
		t.Fatalf("ExpireDropPoints: %v", err)
	}
	if len(affected) != 1 || affected[0].ID != expired.ID {
		t.Fatalf("affected = %+v, want only %s", affected, expired.ID)
	}
	gotExpired, err := repo.FindDropPointByID(context.Background(), expired.ID)
	if err != nil {
		t.Fatalf("Find expired: %v", err)
	}
	if gotExpired.Status != droppoint.StatusExpired {
		t.Fatalf("expired status = %q, want expired", gotExpired.Status)
	}
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	return NewRepository(db.SQLDB())
}

func testNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}

func testDropPoint(t *testing.T, id string, dropPlain string, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:              id,
		APITokenID:      "desktop-main",
		DropTokenHash:   token.HashSecret(dropPlain),
		PickupTokenHash: token.HashSecret(pickupPlain),
		TTL:             10 * time.Minute,
		MaxBytes:        1024,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	return dp
}
