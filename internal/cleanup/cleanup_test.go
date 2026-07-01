package cleanup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/blobstore"
	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/store"
	"github.com/shunichironomura/drop-point/internal/token"
)

func TestExpireDeletesExpiredPayloadsIdempotently(t *testing.T) {
	repo, blobs := newCleanupStore(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := cleanupDropPoint(t, "dp_cleanup_ready", "drop_cleanup", "pick_cleanup", now.Add(-20*time.Minute))
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
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
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	result, err := (Service{Repository: repo, BlobStore: blobs, Now: func() time.Time { return now }}).Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if result.ExpiredDropPoints != 1 || result.DeletedPayloads != 0 {
		t.Fatalf("cleanup result = %+v", result)
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
