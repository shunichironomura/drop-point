package storage

import (
	"database/sql"
	"fmt"
)

// migrations holds ordered groups of DDL statements. Group index i corresponds
// to schema version i+1. PRAGMA user_version records the highest applied
// version, so startup applies only pending groups and is safe to re-run.
//
// Migrations are append-only: never edit or reorder an existing group once it
// has shipped; add a new group instead.
var migrations = [][]string{
	// v1: initial drop_points table (SPEC §8) plus indexes supporting quota
	// counting and expiry scans used by later phases.
	{
		`CREATE TABLE drop_points (
			id                 TEXT    PRIMARY KEY,
			api_token_id       TEXT    NOT NULL,
			client_name        TEXT,
			drop_token_hash    TEXT    NOT NULL UNIQUE,
			pickup_token_hash  TEXT    NOT NULL,
			status             TEXT    NOT NULL CHECK (status IN (
			                       'open', 'receiving', 'ready', 'closed', 'expired', 'failed')),
			payload_path       TEXT,
			envelope_path      TEXT,
			encrypted_size     INTEGER CHECK (encrypted_size IS NULL OR encrypted_size >= 0),
			max_bytes          INTEGER NOT NULL CHECK (max_bytes > 0),
			created_at         INTEGER NOT NULL,
			dropped_at         INTEGER,
			first_picked_up_at INTEGER,
			closed_at          INTEGER,
			expires_at         INTEGER NOT NULL
		) STRICT`,
		`CREATE INDEX idx_drop_points_api_token_status ON drop_points (api_token_id, status)`,
		`CREATE INDEX idx_drop_points_status_expires ON drop_points (status, expires_at)`,
	},
}

// migrate brings the database schema up to the version the binary knows about.
// Each migration group runs inside its own transaction together with the
// user_version bump, so a failure leaves the schema at the last good version.
func migrate(db *sql.DB) error {
	version, err := schemaVersion(db)
	if err != nil {
		return err
	}
	if version < 0 || version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this binary supports (%d)", version, len(migrations))
	}
	for v := version; v < len(migrations); v++ {
		if err := applyMigration(db, v); err != nil {
			return fmt.Errorf("apply migration to v%d: %w", v+1, err)
		}
	}
	return nil
}

func schemaVersion(db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

func applyMigration(db *sql.DB, idx int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range migrations[idx] {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	// PRAGMA user_version does not accept bound parameters; idx+1 is an internal
	// constant, not user input.
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", idx+1)); err != nil {
		return err
	}
	return tx.Commit()
}
