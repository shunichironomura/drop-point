package cleanup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

const (
	cleanupSubmissionOne = "sub_AAAAAAAAAAAAAAAAAAAAAA"
	cleanupSubmissionTwo = "sub_AQEBAQEBAQEBAQEBAQEBAQ"
)

func TestExpireDeletesAllPayloadsForExpiredSessionIdempotently(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := cleanupNow()
	dp := cleanupDropPoint(t, "dp_cleanup_expired", "drop_cleanup", "pick_cleanup", now.Add(-20*time.Minute))
	insertCleanupDropPoint(t, repo, dp)
	readyCleanupSubmission(t, repo, blobs, dp, cleanupSubmissionOne, now.Add(-19*time.Minute))

	service := Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}
	result, err := service.Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.ExpiredDropPoints != 1 || result.DeletedPayloads != 1 {
		t.Fatalf("cleanup result = %+v", result)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop dir stat error = %v, want not exist", err)
	}
	expired, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil || expired.Status != droppoint.StatusExpired {
		t.Fatalf("expired drop point = %+v, err=%v", expired, err)
	}
	submission, err := repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne)
	if err != nil || submission.PayloadPath != "" || submission.EnvelopePath != "" {
		t.Fatalf("cleaned submission = %+v, err=%v", submission, err)
	}

	result, err = service.Expire(context.Background())
	if err != nil || result.ExpiredDropPoints != 0 || result.DeletedPayloads != 0 {
		t.Fatalf("second cleanup = %+v, err=%v", result, err)
	}
}

func TestExpireDeletesAcknowledgedSubmissionButKeepsSessionReusable(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := cleanupNow()
	dp := cleanupDropPoint(t, "dp_cleanup_ack", "drop_ack", "pick_ack", now)
	insertCleanupDropPoint(t, repo, dp)
	readyCleanupSubmission(t, repo, blobs, dp, cleanupSubmissionOne, now)
	if err := repo.AcknowledgeSubmission(context.Background(), dp.ID, cleanupSubmissionOne, now.Add(time.Second)); err != nil {
		t.Fatalf("AcknowledgeSubmission: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now.Add(2 * time.Second) }}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.DeletedPayloads != 1 || result.ExpiredDropPoints != 0 {
		t.Fatalf("cleanup result = %+v", result)
	}
	if err := repo.BeginSubmission(context.Background(), dp.ID, cleanupSubmissionTwo, now.Add(3*time.Second)); err != nil {
		t.Fatalf("session was not reusable after acknowledged cleanup: %v", err)
	}
}

func TestExpireRetriesAcknowledgedSubmissionCleanupFailures(t *testing.T) {
	t.Run("blob deletion", func(t *testing.T) {
		repo, blobs := newCleanupStore(t)
		now := cleanupNow()
		dp := cleanupDropPoint(t, "dp_cleanup_delete_retry", "drop_delete_retry", "pick_delete_retry", now)
		insertCleanupDropPoint(t, repo, dp)
		readyCleanupSubmission(t, repo, blobs, dp, cleanupSubmissionOne, now)
		if err := repo.AcknowledgeSubmission(context.Background(), dp.ID, cleanupSubmissionOne, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		failing := &failOnceSubmissionDeleteStore{Store: blobs}
		service := Service{Repository: repo, BlobStore: failing, Now: func() time.Time { return now.Add(2 * time.Second) }}

		if _, err := service.Expire(context.Background()); err == nil {
			t.Fatal("Expire succeeded on injected blob deletion failure")
		}
		submission, err := repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne)
		if err != nil || submission.PayloadPath == "" || submission.EnvelopePath == "" {
			t.Fatalf("submission pointers were cleared after failed deletion: %+v, err=%v", submission, err)
		}
		if _, err := service.Expire(context.Background()); err != nil {
			t.Fatalf("Expire retry: %v", err)
		}
		submission, err = repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne)
		if err != nil || submission.PayloadPath != "" || submission.EnvelopePath != "" {
			t.Fatalf("submission was not cleaned on retry: %+v, err=%v", submission, err)
		}
	})

	t.Run("pointer clear", func(t *testing.T) {
		db, repo, blobs := newCleanupStoreWithDB(t)
		now := cleanupNow()
		dp := cleanupDropPoint(t, "dp_cleanup_pointer_retry", "drop_pointer_retry", "pick_pointer_retry", now)
		insertCleanupDropPoint(t, repo, dp)
		readyCleanupSubmission(t, repo, blobs, dp, cleanupSubmissionOne, now)
		if err := repo.AcknowledgeSubmission(context.Background(), dp.ID, cleanupSubmissionOne, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQLDB().ExecContext(context.Background(), `
CREATE TRIGGER fail_submission_pointer_clear
BEFORE UPDATE OF envelope_path ON submissions
WHEN OLD.drop_point_id = 'dp_cleanup_pointer_retry' AND OLD.id = 'sub_AAAAAAAAAAAAAAAAAAAAAA' AND NEW.envelope_path IS NULL
BEGIN
  SELECT RAISE(FAIL, 'injected pointer clear failure');
END`); err != nil {
			t.Fatal(err)
		}
		service := Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now.Add(2 * time.Second) }}

		if _, err := service.Expire(context.Background()); err == nil {
			t.Fatal("Expire succeeded on injected pointer-clear failure")
		}
		submission, err := repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne)
		if err != nil || submission.PayloadPath == "" || submission.EnvelopePath == "" {
			t.Fatalf("submission pointers were not left retryable: %+v, err=%v", submission, err)
		}
		if _, err := os.Stat(blobs.SubmissionDir(dp.ID, cleanupSubmissionOne)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("submission directory still exists after successful deletion: %v", err)
		}
		if _, err := db.SQLDB().ExecContext(context.Background(), `DROP TRIGGER fail_submission_pointer_clear`); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Expire(context.Background()); err != nil {
			t.Fatalf("Expire retry: %v", err)
		}
		submission, err = repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne)
		if err != nil || submission.PayloadPath != "" || submission.EnvelopePath != "" {
			t.Fatalf("submission pointers were not cleared on retry: %+v, err=%v", submission, err)
		}
	})
}

func TestReconcileStartupRemovesInterruptedSubmissionsOnly(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := cleanupNow()
	dp := cleanupDropPoint(t, "dp_cleanup_interrupted", "drop_interrupted", "pick_interrupted", now)
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginSubmission(context.Background(), dp.ID, cleanupSubmissionOne, now); err != nil {
		t.Fatalf("BeginSubmission: %v", err)
	}
	if _, err := blobs.WriteSubmission(context.Background(), dp.ID, cleanupSubmissionOne, []byte(`{}`), bytes.NewReader([]byte("partial")), 100); err != nil {
		t.Fatalf("WriteSubmission: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}).ReconcileStartup(context.Background())
	if err != nil {
		t.Fatalf("ReconcileStartup: %v", err)
	}
	if result.RecoveredReceiving != 1 {
		t.Fatalf("RecoveredReceiving = %d, want 1", result.RecoveredReceiving)
	}
	if _, err := repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		t.Fatalf("FindSubmission error = %v, want not found", err)
	}
	if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), dp.DropTokenHash, now); err != nil {
		t.Fatalf("parent session no longer open: %v", err)
	}
}

func TestExpireRecoversOnlyStaleReceivingSubmissions(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := cleanupNow()
	dp := cleanupDropPoint(t, "dp_cleanup_stale", "drop_stale", "pick_stale", now)
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginSubmission(context.Background(), dp.ID, cleanupSubmissionOne, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.BeginSubmission(context.Background(), dp.ID, cleanupSubmissionTwo, now); err != nil {
		t.Fatal(err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }, ReceivingStaleAfter: time.Hour}).Expire(context.Background())
	if err != nil || result.RecoveredReceiving != 1 {
		t.Fatalf("cleanup result = %+v, err=%v", result, err)
	}
	if _, err := repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionOne); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		t.Fatalf("stale submission error = %v", err)
	}
	active, err := repo.FindSubmission(context.Background(), dp.ID, cleanupSubmissionTwo)
	if err != nil || active.Status != droppoint.SubmissionStatusReceiving {
		t.Fatalf("active submission = %+v, err=%v", active, err)
	}
}

func TestExpireDeletesOrphanBlobDirectories(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	orphanDir := blobs.DropDir("dp_cleanup_orphan")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("MkdirAll orphan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, blobstore.PayloadFileName), []byte("orphan"), 0o600); err != nil {
		t.Fatalf("WriteFile orphan: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs}).Expire(context.Background())
	if err != nil || result.DeletedOrphans != 1 {
		t.Fatalf("cleanup result = %+v, err=%v", result, err)
	}
	if _, err := os.Stat(orphanDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan dir stat error = %v", err)
	}
}

func TestExpirePurgesTerminalSessionsAfterRetention(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := cleanupNow()
	old := cleanupDropPoint(t, "dp_cleanup_old", "drop_old", "pick_old", now.Add(-48*time.Hour))
	recent := cleanupDropPoint(t, "dp_cleanup_recent", "drop_recent", "pick_recent", now.Add(-5*time.Minute))
	for _, dp := range []droppoint.DropPoint{old, recent} {
		insertCleanupDropPoint(t, repo, dp)
	}
	if err := repo.CloseDropPoint(context.Background(), old.ID, now.Add(-47*time.Hour-55*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CloseDropPoint(context.Background(), recent.ID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }, TerminalRetention: 24 * time.Hour}).Expire(context.Background())
	if err != nil || result.PurgedRows != 1 {
		t.Fatalf("cleanup result = %+v, err=%v", result, err)
	}
	if _, err := repo.FindDropPointByID(context.Background(), old.ID); !errors.Is(err, droppoint.ErrDropPointNotFound) {
		t.Fatalf("old session error = %v", err)
	}
	if _, err := repo.FindDropPointByID(context.Background(), recent.ID); err != nil {
		t.Fatalf("recent session purged early: %v", err)
	}
}

type failOnceSubmissionDeleteStore struct {
	*blobstore.Store
	failed bool
}

func (s *failOnceSubmissionDeleteStore) DeleteSubmission(ctx context.Context, dropPointID, submissionID string) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected submission deletion failure")
	}
	return s.Store.DeleteSubmission(ctx, dropPointID, submissionID)
}

func newCleanupStore(t *testing.T) (*store.Repository, *blobstore.Store) {
	t.Helper()
	_, repo, blobs := newCleanupStoreWithDB(t)
	return repo, blobs
}

func newCleanupStoreWithDB(t *testing.T) (*store.DB, *store.Repository, *blobstore.Store) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	db, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, store.NewRepository(db.SQLDB()), blobstore.New(dataDir)
}

func readyCleanupSubmission(t *testing.T, repo *store.Repository, blobs *blobstore.Store, dp droppoint.DropPoint, submissionID string, now time.Time) {
	t.Helper()
	if err := repo.BeginSubmission(context.Background(), dp.ID, submissionID, now); err != nil {
		t.Fatalf("BeginSubmission: %v", err)
	}
	stored, err := blobs.WriteSubmission(context.Background(), dp.ID, submissionID, []byte(`{}`), bytes.NewReader([]byte("payload")), dp.MaxBytes)
	if err != nil {
		t.Fatalf("WriteSubmission: %v", err)
	}
	if err := repo.CommitSubmission(context.Background(), dp.ID, submissionID, stored, now.Add(time.Second)); err != nil {
		t.Fatalf("CommitSubmission: %v", err)
	}
}

func insertCleanupDropPoint(t *testing.T, repo *store.Repository, dp droppoint.DropPoint) {
	t.Helper()
	if err := repo.CreateDropPointWithinQuota(context.Background(), dp, 1_000_000, dp.CreatedAt); err != nil {
		t.Fatalf("CreateDropPointWithinQuota %s: %v", dp.ID, err)
	}
}

func cleanupDropPoint(t *testing.T, id, dropPlain, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:                    id,
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         token.HashSecret(dropPlain),
		PickupTokenHash:       token.HashSecret(pickupPlain),
		TTL:                   10 * time.Minute,
		MaxBytes:              1024,
		MaxPendingSubmissions: 4,
		MaxPendingBytes:       4096,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	return dp
}

func cleanupNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}
