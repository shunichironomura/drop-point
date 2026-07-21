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

func TestExpireDeletesExpiredPayloadsIdempotently(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := cleanupDropPoint(t, "dp_cleanup_ready", "drop_cleanup", "pick_cleanup", now.Add(-20*time.Minute))
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now.Add(-19*time.Minute)); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	result, err := blobs.WriteDrop(context.Background(), dp.ID, []byte(`{}`), bytes.NewReader([]byte("payload")), 100)
	if err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), dp.ID, result, now.Add(-18*time.Minute)); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}

	service := Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}
	cleanupResult, err := service.Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if cleanupResult.ExpiredDropPoints != 1 || cleanupResult.DeletedPayloads != 1 {
		t.Fatalf("cleanup result = %+v", cleanupResult)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop dir stat err = %v, want not exist", err)
	}
	expired, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if expired.Status != droppoint.StatusExpired || expired.PayloadPath != "" || expired.EnvelopePath != "" {
		t.Fatalf("expired row mismatch: %+v", expired)
	}

	cleanupResult, err = service.Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire second run: %v", err)
	}
	if cleanupResult.ExpiredDropPoints != 0 || cleanupResult.DeletedPayloads != 0 {
		t.Fatalf("second cleanup result = %+v, want zero", cleanupResult)
	}
}

func TestExpireReconcilesPreMarkedTerminalRows(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := cleanupDropPoint(t, "dp_cleanup_premarked", "drop_premarked", "pick_premarked", now.Add(-20*time.Minute))
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now.Add(-19*time.Minute)); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	stored, err := blobs.WriteDrop(context.Background(), dp.ID, []byte(`{}`), bytes.NewReader([]byte("payload")), 100)
	if err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), dp.ID, stored, now.Add(-18*time.Minute)); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}
	if _, err := repo.AuthorizePickupToken(context.Background(), dp.ID, token.HashSecret("pick_premarked"), now); err != nil {
		t.Fatalf("AuthorizePickupToken: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.ExpiredDropPoints != 0 || result.DeletedPayloads != 1 {
		t.Fatalf("cleanup result = %+v, want pre-marked payload reconciled", result)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop dir stat err = %v, want not exist", err)
	}
}

func TestExpireRetriesDeleteAndPointerClearFailures(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		repo, blobs := newCleanupStore(t)
		now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
		dp := readyCleanupDropPoint(t, repo, blobs, "dp_cleanup_delete_retry", now)
		failing := &failOnceDeleteStore{Store: blobs}
		service := Service{Repository: repo, BlobStore: failing, Now: func() time.Time { return now }}

		if _, err := service.Expire(context.Background()); err == nil {
			t.Fatal("Expire succeeded on injected delete failure")
		}
		if _, err := os.Stat(blobs.DropDir(dp.ID)); err != nil {
			t.Fatalf("drop dir after failed delete: %v", err)
		}
		if _, err := service.Expire(context.Background()); err != nil {
			t.Fatalf("Expire retry: %v", err)
		}
		if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("drop dir stat err = %v, want not exist", err)
		}
	})

	t.Run("pointer clear", func(t *testing.T) {
		db, repo, blobs := newCleanupStoreWithDB(t)
		now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
		dp := readyCleanupDropPoint(t, repo, blobs, "dp_cleanup_pointer_retry", now)
		if _, err := db.SQLDB().ExecContext(context.Background(), `
CREATE TRIGGER fail_pointer_clear
BEFORE UPDATE OF payload_path ON drop_points
WHEN OLD.id = 'dp_cleanup_pointer_retry' AND NEW.payload_path IS NULL
BEGIN
  SELECT RAISE(FAIL, 'injected pointer clear failure');
END`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}
		service := Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}

		if _, err := service.Expire(context.Background()); err == nil {
			t.Fatal("Expire succeeded on injected pointer-clear failure")
		}
		row, err := repo.FindDropPointByID(context.Background(), dp.ID)
		if err != nil {
			t.Fatalf("FindDropPointByID: %v", err)
		}
		if row.PayloadPath == "" || row.EnvelopePath == "" {
			t.Fatalf("pointers cleared despite transaction failure: %+v", row)
		}
		if _, err := db.SQLDB().ExecContext(context.Background(), `DROP TRIGGER fail_pointer_clear`); err != nil {
			t.Fatalf("drop trigger: %v", err)
		}
		if _, err := service.Expire(context.Background()); err != nil {
			t.Fatalf("Expire retry: %v", err)
		}
		row, err = repo.FindDropPointByID(context.Background(), dp.ID)
		if err != nil {
			t.Fatalf("FindDropPointByID after retry: %v", err)
		}
		if row.PayloadPath != "" || row.EnvelopePath != "" {
			t.Fatalf("pointers remain after retry: %+v", row)
		}
	})
}

func TestExpireRetriesAfterCancellation(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := readyCleanupDropPoint(t, repo, blobs, "dp_cleanup_cancel_retry", now)
	ctx, cancel := context.WithCancel(context.Background())
	canceling := &cancelOnceDeleteStore{Store: blobs, cancel: cancel}
	service := Service{Repository: repo, BlobStore: canceling, Now: func() time.Time { return now }}

	if _, err := service.Expire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Expire err = %v, want context.Canceled", err)
	}
	if _, err := service.Expire(context.Background()); err != nil {
		t.Fatalf("Expire retry: %v", err)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop dir stat err = %v, want not exist", err)
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
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.DeletedOrphans != 1 {
		t.Fatalf("DeletedOrphans = %d, want 1", result.DeletedOrphans)
	}
	if _, err := os.Stat(orphanDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan dir stat err = %v, want not exist", err)
	}
}

func TestReconcileStartupRecoversInterruptedReceiving(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := cleanupDropPoint(t, "dp_cleanup_interrupted", "drop_interrupted", "pick_interrupted", now)
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	if _, err := blobs.WriteDrop(context.Background(), dp.ID, []byte(`{}`), bytes.NewReader([]byte("partial")), 100); err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}).ReconcileStartup(context.Background())
	if err != nil {
		t.Fatalf("ReconcileStartup: %v", err)
	}
	if result.RecoveredReceiving != 1 {
		t.Fatalf("RecoveredReceiving = %d, want 1", result.RecoveredReceiving)
	}
	row, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != droppoint.StatusOpen || row.ReceivingStartedAt != nil {
		t.Fatalf("recovered row = %+v, want open without receiving lease", row)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop dir stat err = %v, want not exist", err)
	}
}

func TestExpireRecoversOnlyStaleReceiving(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	stale := cleanupDropPoint(t, "dp_cleanup_stale", "drop_stale", "pick_stale", now)
	active := cleanupDropPoint(t, "dp_cleanup_active_receiving", "drop_active_receiving", "pick_active_receiving", now)
	for _, dp := range []droppoint.DropPoint{stale, active} {
		insertCleanupDropPoint(t, repo, dp)
	}
	if err := repo.BeginReceivingDrop(context.Background(), stale.ID, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("BeginReceivingDrop stale: %v", err)
	}
	if err := repo.BeginReceivingDrop(context.Background(), active.ID, now); err != nil {
		t.Fatalf("BeginReceivingDrop active: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }, ReceivingStaleAfter: time.Hour}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.RecoveredReceiving != 1 {
		t.Fatalf("RecoveredReceiving = %d, want 1", result.RecoveredReceiving)
	}
	staleRow, err := repo.FindDropPointByID(context.Background(), stale.ID)
	if err != nil {
		t.Fatalf("Find stale: %v", err)
	}
	activeRow, err := repo.FindDropPointByID(context.Background(), active.ID)
	if err != nil {
		t.Fatalf("Find active: %v", err)
	}
	if staleRow.Status != droppoint.StatusOpen || activeRow.Status != droppoint.StatusReceiving {
		t.Fatalf("stale status=%q active status=%q", staleRow.Status, activeRow.Status)
	}
}

func TestExpireReconcilesFailedPayload(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := cleanupDropPoint(t, "dp_cleanup_failed", "drop_cleanup_failed", "pick_cleanup_failed", now)
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	stored, err := blobs.WriteDrop(context.Background(), dp.ID, []byte(`{}`), bytes.NewReader([]byte("payload")), 100)
	if err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), dp.ID, stored, now); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}
	if err := repo.FailDropPoint(context.Background(), dp.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("FailDropPoint: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now.Add(2 * time.Second) }}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.DeletedPayloads != 1 {
		t.Fatalf("DeletedPayloads = %d, want 1", result.DeletedPayloads)
	}
	row, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != droppoint.StatusFailed || row.PayloadPath != "" || row.EnvelopePath != "" || row.FailedAt == nil {
		t.Fatalf("failed cleanup row = %+v", row)
	}
}

func TestExpireOpenDropPointWithoutFiles(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := cleanupDropPoint(t, "dp_cleanup_open", "drop_open", "pick_open", now.Add(-20*time.Minute))
	insertCleanupDropPoint(t, repo, dp)
	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.ExpiredDropPoints != 1 || result.DeletedPayloads != 0 {
		t.Fatalf("cleanup result = %+v", result)
	}
}

func TestExpirePurgesTerminalRowsAfterRetention(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	oldClosed := cleanupDropPoint(t, "dp_cleanup_old_closed", "drop_old_closed", "pick_old_closed", now.Add(-48*time.Hour))
	oldExpired := cleanupDropPoint(t, "dp_cleanup_old_expired", "drop_old_expired", "pick_old_expired", now.Add(-48*time.Hour))
	oldFailed := cleanupDropPoint(t, "dp_cleanup_old_failed", "drop_old_failed", "pick_old_failed", now.Add(-48*time.Hour))
	newClosed := cleanupDropPoint(t, "dp_cleanup_new_closed", "drop_new_closed", "pick_new_closed", now.Add(-5*time.Minute))
	for _, dp := range []droppoint.DropPoint{oldClosed, oldExpired, oldFailed, newClosed} {
		insertCleanupDropPoint(t, repo, dp)
	}
	if err := repo.CloseDropPoint(context.Background(), oldClosed.ID, now.Add(-47*time.Hour-55*time.Minute)); err != nil {
		t.Fatalf("CloseDropPoint old: %v", err)
	}
	if err := repo.FailDropPoint(context.Background(), oldFailed.ID, now.Add(-47*time.Hour-55*time.Minute)); err != nil {
		t.Fatalf("FailDropPoint old: %v", err)
	}
	if err := repo.CloseDropPoint(context.Background(), newClosed.ID, now.Add(-4*time.Minute)); err != nil {
		t.Fatalf("CloseDropPoint new: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }, TerminalRetention: 24 * time.Hour}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.ExpiredDropPoints != 1 || result.PurgedRows != 3 {
		t.Fatalf("cleanup result = %+v, want one newly expired and three purged", result)
	}
	for _, id := range []string{oldClosed.ID, oldExpired.ID, oldFailed.ID} {
		if _, err := repo.FindDropPointByID(context.Background(), id); !errors.Is(err, droppoint.ErrDropPointNotFound) {
			t.Fatalf("FindDropPointByID(%s) err = %v, want ErrDropPointNotFound", id, err)
		}
	}
	if _, err := repo.FindDropPointByID(context.Background(), newClosed.ID); err != nil {
		t.Fatalf("new closed row was purged early: %v", err)
	}
}

type failOnceDeleteStore struct {
	*blobstore.Store
	failed bool
}

func (s *failOnceDeleteStore) DeleteDropPoint(ctx context.Context, id string) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected delete failure")
	}
	return s.Store.DeleteDropPoint(ctx, id)
}

type cancelOnceDeleteStore struct {
	*blobstore.Store
	cancel   context.CancelFunc
	canceled bool
}

func (s *cancelOnceDeleteStore) DeleteDropPoint(ctx context.Context, id string) error {
	if !s.canceled {
		s.canceled = true
		s.cancel()
		return ctx.Err()
	}
	return s.Store.DeleteDropPoint(ctx, id)
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

func readyCleanupDropPoint(t *testing.T, repo *store.Repository, blobs *blobstore.Store, id string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp := cleanupDropPoint(t, id, "drop_"+id, "pick_"+id, now.Add(-20*time.Minute))
	insertCleanupDropPoint(t, repo, dp)
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now.Add(-19*time.Minute)); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	stored, err := blobs.WriteDrop(context.Background(), dp.ID, []byte(`{}`), bytes.NewReader([]byte("payload")), 100)
	if err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), dp.ID, stored, now.Add(-18*time.Minute)); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}
	return dp
}

func insertCleanupDropPoint(t *testing.T, repo *store.Repository, dp droppoint.DropPoint) {
	t.Helper()
	if err := repo.CreateDropPointWithinQuota(context.Background(), dp, 1_000_000, dp.CreatedAt); err != nil {
		t.Fatalf("CreateDropPointWithinQuota %s: %v", dp.ID, err)
	}
}

func cleanupDropPoint(t *testing.T, id string, dropPlain string, pickupPlain string, now time.Time) droppoint.DropPoint {
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
