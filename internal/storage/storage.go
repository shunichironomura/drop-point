// Package storage is the imperative-shell persistence layer for the DropPoint
// relay. It owns the data directory, the SQLite connection, and schema
// migrations.
//
// Phase 0 provides connection lifecycle and the initial schema. Higher-level
// repository methods (create, lookup, status transitions) are attached to Store
// in later phases.
package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"
)

const (
	// dataDirPerm restricts the data directory to its owning user. The directory
	// holds the SQLite database (token hashes only) and, in later phases, stored
	// ciphertext, so it must not be group- or world-accessible.
	dataDirPerm os.FileMode = 0o700

	// dbFileName is the SQLite database kept inside the data directory.
	dbFileName = "relay.db"

	// busyTimeoutMS is how long SQLite waits on a locked database before
	// returning SQLITE_BUSY.
	busyTimeoutMS = 5000
)

// Store wraps the relay's SQLite database connection pool.
type Store struct {
	db *sql.DB
}

// Open ensures dataDir exists with restrictive permissions, opens the SQLite
// database inside it with WAL journaling, foreign-key enforcement, and a busy
// timeout, and applies any pending schema migrations.
func Open(dataDir string) (*Store, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("storage: data dir must not be empty")
	}
	if err := EnsureDataDir(dataDir); err != nil {
		return nil, err
	}

	dbPath := filepath.Join(dataDir, dbFileName)
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

// EnsureDataDir creates the data directory (and any missing parents) and
// enforces owner-only permissions on the leaf directory. It is safe to call when
// the directory already exists.
func EnsureDataDir(dataDir string) error {
	if err := os.MkdirAll(dataDir, dataDirPerm); err != nil {
		return fmt.Errorf("storage: create data dir %q: %w", dataDir, err)
	}
	// MkdirAll is subject to the process umask, so set the mode explicitly to
	// guarantee the leaf directory is not group- or world-accessible even if it
	// already existed with looser permissions.
	if err := os.Chmod(dataDir, dataDirPerm); err != nil {
		return fmt.Errorf("storage: set data dir permissions on %q: %w", dataDir, err)
	}
	return nil
}

func openDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", buildDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("storage: open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("storage: connect database: %w", err)
	}
	return db, nil
}

// buildDSN constructs a modernc.org/sqlite DSN that applies WAL journaling,
// foreign-key enforcement, and a busy timeout to every pooled connection. The
// driver splits the DSN on the first '?', treating the left side as the literal
// file path and parsing the right side as URL-encoded query parameters; each
// repeated _pragma value is executed as a PRAGMA on connection open.
func buildDSN(dbPath string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	return dbPath + "?" + q.Encode()
}
