package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const (
	databaseFileName = "relay.db"
	busyTimeoutMS    = 5000
)

// DB owns the SQLite connection pool used by the relay.
type DB struct {
	db   *sql.DB
	path string
}

// Open opens the relay database under dataDir, configures SQLite runtime
// settings, and applies the built-in schema migration.
func Open(ctx context.Context, dataDir string) (*DB, error) {
	if dataDir == "" {
		return nil, fmt.Errorf("data directory path must not be empty")
	}

	databasePath := filepath.Join(dataDir, databaseFileName)
	sqlDB, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database %q: %w", databasePath, err)
	}
	sqlDB.SetMaxOpenConns(1)

	opened := &DB{db: sqlDB, path: databasePath}
	if err := sqlDB.PingContext(ctx); err != nil {
		opened.Close()
		return nil, fmt.Errorf("ping sqlite database %q: %w", databasePath, err)
	}
	if err := configureSQLite(ctx, sqlDB); err != nil {
		opened.Close()
		return nil, err
	}
	if err := Migrate(ctx, sqlDB); err != nil {
		opened.Close()
		return nil, err
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		opened.Close()
		return nil, fmt.Errorf("set permissions on sqlite database %q: %w", databasePath, err)
	}

	return opened, nil
}

// Close releases database resources.
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// Path returns the SQLite database path.
func (d *DB) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// SQLDB exposes the underlying handle for repository methods and package tests.
func (d *DB) SQLDB() *sql.DB {
	if d == nil {
		return nil
	}
	return d.db
}

func configureSQLite(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMS)); err != nil {
		return fmt.Errorf("configure sqlite busy timeout: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}

	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable sqlite WAL journal mode: %w", err)
	}
	if journalMode != "wal" {
		return fmt.Errorf("enable sqlite WAL journal mode: got %q", journalMode)
	}
	return nil
}
