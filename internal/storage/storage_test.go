package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openTestStore(t *testing.T, dataDir string) *Store {
	t.Helper()
	s, err := Open(dataDir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dataDir, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesDataDirWithRestrictivePerms(t *testing.T) {
	// Use a nested path to prove parents are created too.
	dataDir := filepath.Join(t.TempDir(), "nested", "drop-point")
	s := openTestStore(t, dataDir)

	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q is not a directory", dataDir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("data dir perm = %o, want 700", perm)
	}

	if _, err := os.Stat(filepath.Join(dataDir, dbFileName)); err != nil {
		t.Errorf("relay.db not created: %v", err)
	}

	// Sanity: the store is usable.
	if err := s.db.Ping(); err != nil {
		t.Errorf("ping: %v", err)
	}
}

func TestConnectionPragmas(t *testing.T) {
	s := openTestStore(t, t.TempDir())

	var journal string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if !strings.EqualFold(journal, "wal") {
		t.Errorf("journal_mode = %q, want wal", journal)
	}

	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var busyTimeout int
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != busyTimeoutMS {
		t.Errorf("busy_timeout = %d, want %d", busyTimeout, busyTimeoutMS)
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	dataDir := t.TempDir()

	s1 := openTestStore(t, dataDir)
	v1, err := schemaVersion(s1.db)
	if err != nil {
		t.Fatal(err)
	}
	if v1 != len(migrations) {
		t.Fatalf("schema version after first open = %d, want %d", v1, len(migrations))
	}
	_ = s1.Close()

	// Re-opening the same data dir must not re-run migrations or fail.
	s2 := openTestStore(t, dataDir)
	v2, err := schemaVersion(s2.db)
	if err != nil {
		t.Fatal(err)
	}
	if v2 != len(migrations) {
		t.Fatalf("schema version after second open = %d, want %d", v2, len(migrations))
	}
}

func TestDropPointsSchemaAcceptsValidRow(t *testing.T) {
	s := openTestStore(t, t.TempDir())

	_, err := s.db.Exec(`INSERT INTO drop_points
		(id, api_token_id, drop_token_hash, pickup_token_hash, status, max_bytes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"dp_1", "desktop-main", "drophash", "pickhash", "open", 1024, 1000, 1600)
	if err != nil {
		t.Fatalf("insert valid row: %v", err)
	}

	var status string
	if err := s.db.QueryRow(`SELECT status FROM drop_points WHERE id = ?`, "dp_1").Scan(&status); err != nil {
		t.Fatalf("select row: %v", err)
	}
	if status != "open" {
		t.Errorf("status = %q, want open", status)
	}
}

func TestDropPointsRejectsInvalidStatus(t *testing.T) {
	s := openTestStore(t, t.TempDir())

	_, err := s.db.Exec(`INSERT INTO drop_points
		(id, api_token_id, drop_token_hash, pickup_token_hash, status, max_bytes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"dp_bad", "desktop-main", "h1", "h2", "bogus", 1024, 1000, 1600)
	if err == nil {
		t.Fatal("expected CHECK constraint violation for invalid status, got nil")
	}
}

func TestDropPointsRejectsDuplicateDropTokenHash(t *testing.T) {
	s := openTestStore(t, t.TempDir())

	insert := func(id, dropHash string) error {
		_, err := s.db.Exec(`INSERT INTO drop_points
			(id, api_token_id, drop_token_hash, pickup_token_hash, status, max_bytes, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, "desktop-main", dropHash, "pickhash", "open", 1024, 1000, 1600)
		return err
	}
	if err := insert("dp_a", "samehash"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("dp_b", "samehash"); err == nil {
		t.Fatal("expected UNIQUE violation on drop_token_hash, got nil")
	}
}
