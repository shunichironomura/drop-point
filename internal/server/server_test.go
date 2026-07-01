package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shunichironomura/drop-point/internal/config"
)

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
