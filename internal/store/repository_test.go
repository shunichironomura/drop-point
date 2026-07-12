package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestRepositoryCreateLookup(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_one", "drop_one", "pick_one", now)

	insertTestDropPoint(t, repo, dp)

	got, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if got.ID != dp.ID || got.DisplayName != dp.DisplayName || got.DropTokenHash != dp.DropTokenHash || got.PickupTokenHash != dp.PickupTokenHash {
		t.Fatalf("loaded drop point mismatch: %+v", got)
	}
}

func TestRepositoryCreateDropPointWithinQuota(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	first := testDropPoint(t, "dp_quota_one", "drop_quota_one", "pick_quota_one", now)
	second := testDropPoint(t, "dp_quota_two", "drop_quota_two", "pick_quota_two", now)

	if err := repo.CreateDropPointWithinQuota(context.Background(), first, 1, now); err != nil {
		t.Fatalf("CreateDropPointWithinQuota first: %v", err)
	}
	if err := repo.CreateDropPointWithinQuota(context.Background(), second, 1, now); !errors.Is(err, droppoint.ErrActiveDropPointQuotaExceeded) {
		t.Fatalf("CreateDropPointWithinQuota second err = %v, want ErrActiveDropPointQuotaExceeded", err)
	}
	if _, err := repo.FindDropPointByID(context.Background(), first.ID); err != nil {
		t.Fatalf("FindDropPointByID first: %v", err)
	}
}

func TestRepositoryCreateDropPointWithinQuotaConcurrent(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	const attempts = 8

	dropPoints := make([]droppoint.DropPoint, 0, attempts)
	for i := range attempts {
		suffix := string(rune('a' + i))
		dropPoints = append(dropPoints, testDropPoint(t, "dp_quota_race_"+suffix, "drop_quota_race_"+suffix, "pick_quota_race_"+suffix, now))
	}

	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for _, dp := range dropPoints {
		wg.Add(1)
		go func(dp droppoint.DropPoint) {
			defer wg.Done()
			errs <- repo.CreateDropPointWithinQuota(context.Background(), dp, 1, now)
		}(dp)
	}
	wg.Wait()
	close(errs)

	successes := 0
	quotaExceeded := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, droppoint.ErrActiveDropPointQuotaExceeded):
			quotaExceeded++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if successes != 1 || quotaExceeded != attempts-1 {
		t.Fatalf("successes=%d quotaExceeded=%d, want 1/%d", successes, quotaExceeded, attempts-1)
	}
}

func TestRepositoryDetectsUniqueConstraintStructurally(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_unique", "drop_unique", "pick_unique", now)
	insertTestDropPoint(t, repo, dp)

	duplicateID := testDropPoint(t, "dp_unique", "drop_unique_other", "pick_unique_other", now)
	if err := repo.CreateDropPointWithinQuota(context.Background(), duplicateID, 1_000_000, now); !IsUniqueConstraint(err) {
		t.Fatalf("duplicate ID err = %v, want structural unique constraint", err)
	}

	duplicateDropToken := testDropPoint(t, "dp_unique_other", "drop_unique", "pick_unique_other_two", now)
	if err := repo.CreateDropPointWithinQuota(context.Background(), duplicateDropToken, 1_000_000, now); !IsUniqueConstraint(err) {
		t.Fatalf("duplicate drop token err = %v, want structural unique constraint", err)
	}
}

func TestRepositoryDropTokenLookupAndReceivingAbort(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_drop", "drop_secret", "pick_secret", now)
	insertTestDropPoint(t, repo, dp)

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
	receiving, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("Find receiving: %v", err)
	}
	if receiving.ReceivingStartedAt == nil || !receiving.ReceivingStartedAt.Equal(now) {
		t.Fatalf("receiving_started_at = %v, want %v", receiving.ReceivingStartedAt, now)
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
	if again.Status != droppoint.StatusOpen || again.ReceivingStartedAt != nil {
		t.Fatalf("row after reset = %+v, want open without receiving lease", again)
	}
}

func TestRepositoryPickupAuthorizationIsScopedToDropPoint(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp1 := testDropPoint(t, "dp_one", "drop_one", "pick_one", now)
	dp2 := testDropPoint(t, "dp_two", "drop_two", "pick_two", now)
	for _, dp := range []droppoint.DropPoint{dp1, dp2} {
		insertTestDropPoint(t, repo, dp)
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
	insertTestDropPoint(t, repo, dp)
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
		insertTestDropPoint(t, repo, dp)
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

func TestRepositoryPropagatesMarkExpiredFailure(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_expire_failure", "drop_expire_failure", "pick_expire_failure", now.Add(-20*time.Minute))
	insertTestDropPoint(t, repo, dp)
	if _, err := repo.db.ExecContext(context.Background(), `
CREATE TRIGGER fail_mark_expired
BEFORE UPDATE OF status ON drop_points
WHEN OLD.id = 'dp_expire_failure' AND NEW.status = 'expired'
BEGIN
  SELECT RAISE(FAIL, 'injected mark-expired failure');
END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := repo.FindDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_expire_failure"), now); err == nil || !strings.Contains(err.Error(), "mark drop point expired") {
		t.Fatalf("FindDropPointByDropTokenHash err = %v, want persisted expiry error", err)
	}
	row, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != droppoint.StatusOpen {
		t.Fatalf("status = %q, want open after failed expiry update", row.Status)
	}
}

func TestParseTimeRequiresSQLiteFormat(t *testing.T) {
	if _, err := parseTime(testNow().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("parseTime accepted broad RFC3339Nano timestamp, want strict sqlite format")
	}
	if _, err := parseTime(formatTime(testNow())); err != nil {
		t.Fatalf("parseTime rejected formatTime output: %v", err)
	}
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	return NewRepository(db.SQLDB())
}

func insertTestDropPoint(t *testing.T, repo *Repository, dp droppoint.DropPoint) {
	t.Helper()
	if err := repo.CreateDropPointWithinQuota(context.Background(), dp, 1_000_000, dp.CreatedAt); err != nil {
		t.Fatalf("CreateDropPointWithinQuota %s: %v", dp.ID, err)
	}
}

func testNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}

func testDropPoint(t *testing.T, id string, dropPlain string, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:              id,
		APITokenID:      "desktop-main",
		DisplayName:     "calm-otter",
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
