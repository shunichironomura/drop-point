package store

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 2

const schemaSQL = `
CREATE TABLE IF NOT EXISTS drop_points (
  id TEXT PRIMARY KEY,
  api_token_id TEXT NOT NULL,
  client_name TEXT,
  display_name TEXT NOT NULL CHECK (length(display_name) > 0),
  drop_token_hash TEXT NOT NULL UNIQUE,
  pickup_token_hash TEXT NOT NULL,
  status TEXT NOT NULL,
  payload_path TEXT,
  envelope_path TEXT,
  encrypted_size INTEGER,
  created_at TEXT NOT NULL,
  dropped_at TEXT,
  receiving_started_at TEXT,
  first_picked_up_at TEXT,
  closed_at TEXT,
  failed_at TEXT,
  expires_at TEXT NOT NULL,
  max_bytes INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_drop_points_status_expires_at
  ON drop_points (status, expires_at);

CREATE INDEX IF NOT EXISTS idx_drop_points_api_token_status
  ON drop_points (api_token_id, status);

CREATE TABLE IF NOT EXISTS api_tokens (
  id TEXT PRIMARY KEY,
  secret_hash TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  max_active_drop_points INTEGER,
  created_at TEXT NOT NULL,
  disabled_at TEXT
);

PRAGMA user_version = 2;
`

// Migrate creates or verifies the current schema. DropPoint is unreleased, so
// unversioned legacy relay tables are rejected instead of being backfilled into
// states that violate current invariants.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlite database handle must not be nil")
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	switch {
	case version == 0:
		hasLegacyTables, err := relayTablesExist(ctx, db)
		if err != nil {
			return err
		}
		if hasLegacyTables {
			return fmt.Errorf("unsupported unversioned DropPoint database; recreate the unreleased database with the current schema")
		}
	case version != schemaVersion:
		return fmt.Errorf("unsupported DropPoint schema version %d, want %d", version, schemaVersion)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("migrate sqlite schema version %d: %w", schemaVersion, err)
	}
	return nil
}

func relayTablesExist(ctx context.Context, db *sql.DB) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `
SELECT count(*)
FROM sqlite_master
WHERE type = 'table' AND name IN ('drop_points', 'api_tokens')`).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect existing relay schema: %w", err)
	}
	return count != 0, nil
}
