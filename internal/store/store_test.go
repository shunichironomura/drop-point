package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/config"
)

func TestOpenConfiguresSQLite(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	var journalMode string
	if err := db.SQLDB().QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := db.SQLDB().QueryRowContext(context.Background(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := db.SQLDB().QueryRowContext(context.Background(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != busyTimeoutMS {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, busyTimeoutMS)
	}
}

func TestMigrationCreatesDropPointsSchema(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for _, name := range []string{
		"drop_points",
		"idx_drop_points_status_expires_at",
		"idx_drop_points_api_token_status",
	} {
		var count int
		if err := db.SQLDB().QueryRowContext(
			context.Background(),
			"SELECT count(*) FROM sqlite_master WHERE name = ?",
			name,
		).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", name, err)
		}
		if count != 1 {
			t.Fatalf("sqlite object %s count = %d, want 1", name, count)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.SQLDB().ExecContext(context.Background(), `
INSERT INTO drop_points (
  id, api_token_id, client_name, drop_token_hash, pickup_token_hash, status,
  created_at, expires_at, max_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"dp_test", "desktop-main", "test-client", "sha256:drop", "sha256:pick", "open", now, now, 1024,
	)
	if err != nil {
		t.Fatalf("insert drop point row: %v", err)
	}
}

func TestOpenCreatesRestrictiveDatabaseFile(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	info, err := os.Stat(db.Path())
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o, want 600", got)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	db, err := Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return db
}
