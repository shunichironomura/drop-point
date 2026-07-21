package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/config"
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
		"api_tokens",
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

	assertDropPointsColumnExists(t, db.SQLDB(), "display_name")
	assertDropPointsColumnExists(t, db.SQLDB(), "receiving_started_at")
	assertDropPointsColumnExists(t, db.SQLDB(), "failed_at")
	var version int
	if err := db.SQLDB().QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	if err := Migrate(context.Background(), db.SQLDB()); err != nil {
		t.Fatalf("idempotent Migrate: %v", err)
	}

	now := formatTime(time.Now().UTC())
	_, err := db.SQLDB().ExecContext(context.Background(), `
INSERT INTO drop_points (
  id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash, status,
  created_at, expires_at, max_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"dp_test", "desktop-main", "test-client", "calm-otter", "sha256:drop", "sha256:pick", "open", now, now, 1024,
	)
	if err != nil {
		t.Fatalf("insert drop point row: %v", err)
	}
	_, err = db.SQLDB().ExecContext(context.Background(), `
INSERT INTO drop_points (
  id, api_token_id, display_name, drop_token_hash, pickup_token_hash, status,
  created_at, expires_at, max_bytes
) VALUES ('dp_empty_name', 'desktop-main', '', 'sha256:drop-empty', 'sha256:pick-empty', 'open', ?, ?, 1024)`, now, now)
	if err == nil {
		t.Fatal("schema accepted an empty display_name")
	}
}

func TestMigrationRejectsUnversionedLegacySchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE drop_points (
  id TEXT PRIMARY KEY,
  api_token_id TEXT NOT NULL,
  client_name TEXT,
  drop_token_hash TEXT NOT NULL UNIQUE,
  pickup_token_hash TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_path TEXT,
  envelope_path TEXT,
  encrypted_size INTEGER,
  created_at TEXT NOT NULL,
  dropped_at TEXT,
  first_picked_up_at TEXT,
  closed_at TEXT,
  expires_at TEXT NOT NULL,
  max_bytes INTEGER NOT NULL
)`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := Migrate(context.Background(), db); err == nil || !strings.Contains(err.Error(), "unsupported unversioned") {
		t.Fatalf("Migrate error = %v, want unsupported legacy schema", err)
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

func TestOpenCreatesRestrictiveLiveWALSidecars(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.SQLDB().ExecContext(context.Background(), `INSERT INTO api_tokens (id, secret_hash, created_at) VALUES ('mode-test', 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '2026-07-01T12:00:00.000000000Z')`); err != nil {
		t.Fatalf("insert WAL row: %v", err)
	}
	for _, path := range []string{db.Path(), db.Path() + "-wal", db.Path() + "-shm"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat live SQLite file %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode(%s) = %o, want 600", path, got)
		}
	}
}

func TestOpenRestrictsExistingDatabaseBeforeWALSetup(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	path := filepath.Join(dataDir, databaseFileName)
	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	db, err := Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %o, want 600", got)
	}
}

func TestOpenTreatsQuestionMarkAsPathLiteral(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data?literal")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	db, err := Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := os.Stat(filepath.Join(dataDir, databaseFileName)); err != nil {
		t.Fatalf("stat database at literal path: %v", err)
	}
}

func assertDropPointsColumnExists(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info(drop_points)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			columnName string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if columnName == name {
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan table_info: %v", err)
	}
	t.Fatalf("drop_points.%s column not found", name)
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
