package server

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

const serverTestSubmissionID = "sub_AAAAAAAAAAAAAAAAAAAAAA"

func TestNewInitializesDataDirStoreAndHealth(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = filepath.Join(t.TempDir(), "data")

	srv, err := New(context.Background(), cfg, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer srv.Close()

	assertMode(t, cfg.DataDir, 0o700)
	assertMode(t, filepath.Join(cfg.DataDir, "drop-points"), 0o700)
	assertMode(t, filepath.Join(cfg.DataDir, "relay.db"), 0o600)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	srv.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestNewConfiguresPublicHTTPTimeouts(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.ReadTimeoutSeconds = 123
	cfg.WriteTimeoutSeconds = 456

	srv, err := New(context.Background(), cfg, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer srv.Close()

	if srv.HTTPServer.ReadHeaderTimeout != readHeaderTimeout {
		t.Fatalf("ReadHeaderTimeout = %s, want %s", srv.HTTPServer.ReadHeaderTimeout, readHeaderTimeout)
	}
	if srv.HTTPServer.ReadTimeout != 123*time.Second {
		t.Fatalf("ReadTimeout = %s, want 123s", srv.HTTPServer.ReadTimeout)
	}
	if srv.HTTPServer.WriteTimeout != 456*time.Second {
		t.Fatalf("WriteTimeout = %s, want 456s", srv.HTTPServer.WriteTimeout)
	}
	if srv.HTTPServer.IdleTimeout != idleTimeout {
		t.Fatalf("IdleTimeout = %s, want %s", srv.HTTPServer.IdleTimeout, idleTimeout)
	}
}

func TestCleanupLoopExpiresAndDeletesPayloads(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = filepath.Join(t.TempDir(), "data")

	srv, err := New(context.Background(), cfg, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer srv.Close()

	createdAt := time.Now().UTC().Add(-20 * time.Minute)
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:                    "dp_server_cleanup",
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         token.HashSecret("drop_server_cleanup"),
		PickupTokenHash:       token.HashSecret("pick_server_cleanup"),
		TTL:                   10 * time.Minute,
		MaxBytes:              1024,
		MaxPendingSubmissions: 10,
		MaxPendingBytes:       10 * 1024,
	}, createdAt)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	if err := srv.Repository.CreateDropPointWithinQuota(context.Background(), dp, 1_000_000, dp.CreatedAt); err != nil {
		t.Fatalf("CreateDropPointWithinQuota: %v", err)
	}
	if err := srv.Repository.BeginSubmission(context.Background(), dp.ID, serverTestSubmissionID, createdAt.Add(time.Minute)); err != nil {
		t.Fatalf("BeginSubmission: %v", err)
	}
	result, err := srv.BlobStore.WriteSubmission(context.Background(), dp.ID, serverTestSubmissionID, []byte(`{}`), bytes.NewReader([]byte("payload")), 1024)
	if err != nil {
		t.Fatalf("WriteSubmission: %v", err)
	}
	if err := srv.Repository.CommitSubmission(context.Background(), dp.ID, serverTestSubmissionID, result, createdAt.Add(2*time.Minute)); err != nil {
		t.Fatalf("CommitSubmission: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startCleanupLoop(ctx)
	defer func() {
		cancel()
		<-done
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := srv.Repository.FindDropPointByID(context.Background(), dp.ID)
		submission, submissionErr := srv.Repository.FindSubmission(context.Background(), dp.ID, serverTestSubmissionID)
		if err == nil && submissionErr == nil && got.Status == droppoint.StatusExpired && submission.PayloadPath == "" && submission.EnvelopePath == "" {
			if _, statErr := os.Stat(srv.BlobStore.DropDir(dp.ID)); os.IsNotExist(statErr) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup loop did not expire and delete payload before deadline")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNewRecoversInterruptedReceivingBeforeServing(t *testing.T) {
	cfg := config.Default()
	cfg.ListenAddr = "127.0.0.1:0"
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	first, err := New(context.Background(), cfg, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	now := time.Now().UTC()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:                    "dp_server_interrupted",
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         token.HashSecret("drop_server_interrupted"),
		PickupTokenHash:       token.HashSecret("pick_server_interrupted"),
		TTL:                   10 * time.Minute,
		MaxBytes:              1024,
		MaxPendingSubmissions: 10,
		MaxPendingBytes:       10 * 1024,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	if err := first.Repository.CreateDropPointWithinQuota(context.Background(), dp, 1_000_000, now); err != nil {
		t.Fatalf("CreateDropPointWithinQuota: %v", err)
	}
	if err := first.Repository.BeginSubmission(context.Background(), dp.ID, serverTestSubmissionID, now); err != nil {
		t.Fatalf("BeginSubmission: %v", err)
	}
	if _, err := first.BlobStore.WriteSubmission(context.Background(), dp.ID, serverTestSubmissionID, []byte(`{}`), bytes.NewReader([]byte("interrupted")), 1024); err != nil {
		t.Fatalf("WriteSubmission: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	second, err := New(context.Background(), cfg, log.New(&bytes.Buffer{}, "", 0))
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer second.Close()
	row, err := second.Repository.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != droppoint.StatusOpen {
		t.Fatalf("startup-recovered row = %+v", row)
	}
	if _, err := second.Repository.FindSubmission(context.Background(), dp.ID, serverTestSubmissionID); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		t.Fatalf("FindSubmission after startup recovery error = %v, want submission not found", err)
	}
	if _, err := os.Stat(second.BlobStore.SubmissionDir(dp.ID, serverTestSubmissionID)); !os.IsNotExist(err) {
		t.Fatalf("interrupted submission dir stat err = %v, want not exist", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	cfg := config.Default()
	cfg.BaseURL = "not-a-url"
	cfg.DataDir = filepath.Join(t.TempDir(), "data")

	srv, err := New(context.Background(), cfg, log.New(&bytes.Buffer{}, "", 0))
	if err == nil {
		srv.Close()
		t.Fatal("New() succeeded, want error")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %o, want %o", path, got, want)
	}
}
