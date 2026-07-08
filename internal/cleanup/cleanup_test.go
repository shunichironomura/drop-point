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
	newClosed := cleanupDropPoint(t, "dp_cleanup_new_closed", "drop_new_closed", "pick_new_closed", now.Add(-5*time.Minute))
	for _, dp := range []droppoint.DropPoint{oldClosed, oldExpired, newClosed} {
		insertCleanupDropPoint(t, repo, dp)
	}
	if err := repo.CloseDropPoint(context.Background(), oldClosed.ID, now.Add(-47*time.Hour-55*time.Minute)); err != nil {
		t.Fatalf("CloseDropPoint old: %v", err)
	}
	if err := repo.CloseDropPoint(context.Background(), newClosed.ID, now.Add(-4*time.Minute)); err != nil {
		t.Fatalf("CloseDropPoint new: %v", err)
	}

	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }, TerminalRetention: 24 * time.Hour}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.ExpiredDropPoints != 1 || result.PurgedRows != 2 {
		t.Fatalf("cleanup result = %+v, want one newly expired and two purged", result)
	}
	for _, id := range []string{oldClosed.ID, oldExpired.ID} {
		if _, err := repo.FindDropPointByID(context.Background(), id); !errors.Is(err, droppoint.ErrDropPointNotFound) {
			t.Fatalf("FindDropPointByID(%s) err = %v, want ErrDropPointNotFound", id, err)
		}
	}
	if _, err := repo.FindDropPointByID(context.Background(), newClosed.ID); err != nil {
		t.Fatalf("new closed row was purged early: %v", err)
	}
}

func newCleanupStore(t *testing.T) (*store.Repository, *blobstore.Store) {
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
	return store.NewRepository(db.SQLDB()), blobstore.New(dataDir)
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
