package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

const (
	testSubmissionOne = "sub_AAAAAAAAAAAAAAAAAAAAAA"
	testSubmissionTwo = "sub_AQEBAQEBAQEBAQEBAQEBAQ"
)

func TestRepositoryCreatesAndAuthorizesReusableDropPoint(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_one", "drop_one", "pick_one", now)
	insertTestDropPoint(t, repo, dp)

	got, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_one"), now)
	if err != nil {
		t.Fatalf("FindOpenDropPointByDropTokenHash: %v", err)
	}
	if got.ID != dp.ID || got.MaxPendingSubmissions != 2 || got.MaxPendingBytes != 12 {
		t.Fatalf("drop point = %+v", got)
	}
	if _, err := repo.AuthorizePickupToken(context.Background(), dp.ID, token.HashSecret("pick_one"), now); err != nil {
		t.Fatalf("AuthorizePickupToken: %v", err)
	}
	if _, err := repo.AuthorizePickupToken(context.Background(), dp.ID, token.HashSecret("wrong"), now); !errors.Is(err, droppoint.ErrPickupTokenInvalid) {
		t.Fatalf("wrong pickup token error = %v", err)
	}
}

func TestRepositoryEnforcesActiveDropPointQuotaConcurrently(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	const attempts = 8
	var wg sync.WaitGroup
	errs := make(chan error, attempts)
	for i := range attempts {
		dp := testDropPoint(t, "dp_quota_"+string(rune('a'+i)), "drop_quota_"+string(rune('a'+i)), "pick_quota_"+string(rune('a'+i)), now)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.CreateDropPointWithinQuota(context.Background(), dp, 1, now)
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, droppoint.ErrActiveDropPointQuotaExceeded) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d, want 1", successes)
	}
}

func TestRepositorySubmissionQueueLifecycle(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_queue", "drop_queue", "pick_queue", now)
	insertTestDropPoint(t, repo, dp)

	if err := repo.BeginSubmission(context.Background(), dp.ID, testSubmissionOne, now); err != nil {
		t.Fatalf("BeginSubmission: %v", err)
	}
	stored := droppoint.CommitSubmissionResult{EnvelopePath: "drop-points/dp_queue/sub/envelope.json", PayloadPath: "drop-points/dp_queue/sub/payload.bin", EncryptedSize: 5}
	if err := repo.CommitSubmission(context.Background(), dp.ID, testSubmissionOne, stored, now.Add(time.Second)); err != nil {
		t.Fatalf("CommitSubmission: %v", err)
	}
	ready, err := repo.ListReadySubmissions(context.Background(), dp.ID)
	if err != nil || len(ready) != 1 || ready[0].ID != testSubmissionOne {
		t.Fatalf("ready submissions = %+v, err=%v", ready, err)
	}
	stats, err := repo.PendingStats(context.Background(), dp.ID)
	if err != nil || stats.Submissions != 1 || stats.Bytes != 5 {
		t.Fatalf("pending stats = %+v, err=%v", stats, err)
	}

	pickedAt := now.Add(2 * time.Second)
	if err := repo.MarkSubmissionPickedUp(context.Background(), dp.ID, testSubmissionOne, pickedAt); err != nil {
		t.Fatalf("MarkSubmissionPickedUp: %v", err)
	}
	if err := repo.AcknowledgeSubmission(context.Background(), dp.ID, testSubmissionOne, now.Add(3*time.Second)); err != nil {
		t.Fatalf("AcknowledgeSubmission: %v", err)
	}
	if err := repo.AcknowledgeSubmission(context.Background(), dp.ID, testSubmissionOne, now.Add(4*time.Second)); err != nil {
		t.Fatalf("AcknowledgeSubmission retry: %v", err)
	}
	acknowledged, err := repo.FindSubmission(context.Background(), dp.ID, testSubmissionOne)
	if err != nil || acknowledged.Status != droppoint.SubmissionStatusAcknowledged || acknowledged.FirstPickedUpAt == nil || !acknowledged.FirstPickedUpAt.Equal(pickedAt) {
		t.Fatalf("acknowledged submission = %+v, err=%v", acknowledged, err)
	}
	stats, err = repo.PendingStats(context.Background(), dp.ID)
	if err != nil || stats.Submissions != 0 || stats.Bytes != 0 {
		t.Fatalf("pending stats after ack = %+v, err=%v", stats, err)
	}
	if err := repo.BeginSubmission(context.Background(), dp.ID, testSubmissionOne, now.Add(5*time.Second)); !errors.Is(err, droppoint.ErrSubmissionAlreadyExists) {
		t.Fatalf("reused immutable ID error = %v", err)
	}
	if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), dp.DropTokenHash, now.Add(5*time.Second)); err != nil {
		t.Fatalf("parent session is no longer reusable: %v", err)
	}
}

func TestRepositoryEnforcesSubmissionCountAndByteBounds(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		repo := newTestRepository(t)
		now := testNow()
		dp := testDropPoint(t, "dp_count", "drop_count", "pick_count", now)
		dp.MaxPendingSubmissions = 1
		insertTestDropPoint(t, repo, dp)
		if err := repo.BeginSubmission(context.Background(), dp.ID, testSubmissionOne, now); err != nil {
			t.Fatal(err)
		}
		stored := droppoint.CommitSubmissionResult{EnvelopePath: "e", PayloadPath: "p", EncryptedSize: 4}
		if err := repo.CommitSubmission(context.Background(), dp.ID, testSubmissionOne, stored, now); err != nil {
			t.Fatal(err)
		}
		if err := repo.BeginSubmission(context.Background(), dp.ID, testSubmissionOne, now); !errors.Is(err, droppoint.ErrSubmissionAlreadyExists) {
			t.Fatalf("immutable retry at count limit error = %v", err)
		}
		if err := repo.BeginSubmission(context.Background(), dp.ID, testSubmissionTwo, now); !errors.Is(err, droppoint.ErrPendingSubmissionQuotaExceeded) {
			t.Fatalf("second submission error = %v", err)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		repo := newTestRepository(t)
		now := testNow()
		dp := testDropPoint(t, "dp_bytes", "drop_bytes", "pick_bytes", now)
		dp.MaxPendingBytes = 6
		insertTestDropPoint(t, repo, dp)
		for _, id := range []string{testSubmissionOne, testSubmissionTwo} {
			if err := repo.BeginSubmission(context.Background(), dp.ID, id, now); err != nil {
				t.Fatal(err)
			}
		}
		stored := droppoint.CommitSubmissionResult{EnvelopePath: "e", PayloadPath: "p", EncryptedSize: 4}
		if err := repo.CommitSubmission(context.Background(), dp.ID, testSubmissionOne, stored, now); err != nil {
			t.Fatal(err)
		}
		if err := repo.CommitSubmission(context.Background(), dp.ID, testSubmissionTwo, stored, now); !errors.Is(err, droppoint.ErrPendingBytesQuotaExceeded) {
			t.Fatalf("byte quota error = %v", err)
		}
	})
}

func TestRepositoryCloseAndExpireSessions(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	closed := testDropPoint(t, "dp_closed", "drop_closed", "pick_closed", now)
	expired := testDropPoint(t, "dp_expired", "drop_expired", "pick_expired", now.Add(-20*time.Minute))
	insertTestDropPoint(t, repo, closed)
	insertTestDropPoint(t, repo, expired)
	if err := repo.CloseDropPoint(context.Background(), closed.ID, now); err != nil {
		t.Fatalf("CloseDropPoint: %v", err)
	}
	affected, err := repo.ExpireDropPoints(context.Background(), now)
	if err != nil || len(affected) != 1 || affected[0].ID != expired.ID {
		t.Fatalf("expired = %+v, err=%v", affected, err)
	}
	if err := repo.BeginSubmission(context.Background(), closed.ID, testSubmissionOne, now); !errors.Is(err, droppoint.ErrDropPointClosed) {
		t.Fatalf("closed begin error = %v", err)
	}
	if err := repo.BeginSubmission(context.Background(), expired.ID, testSubmissionOne, now); !errors.Is(err, droppoint.ErrDropPointExpired) {
		t.Fatalf("expired begin error = %v", err)
	}
}

func TestParseTimeRequiresSQLiteFormat(t *testing.T) {
	if _, err := parseTime(testNow().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("parseTime accepted broad RFC3339Nano timestamp")
	}
	if _, err := parseTime(formatTime(testNow())); err != nil {
		t.Fatalf("parseTime: %v", err)
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

func testDropPoint(t *testing.T, id, dropPlain, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:                    id,
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         token.HashSecret(dropPlain),
		PickupTokenHash:       token.HashSecret(pickupPlain),
		TTL:                   10 * time.Minute,
		MaxBytes:              6,
		MaxPendingSubmissions: 2,
		MaxPendingBytes:       12,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	return dp
}
