package store

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS drop_points (
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
);

CREATE INDEX IF NOT EXISTS idx_drop_points_status_expires_at
  ON drop_points (status, expires_at);

CREATE INDEX IF NOT EXISTS idx_drop_points_api_token_status
  ON drop_points (api_token_id, status);
`

// Migrate applies the current SQLite schema. Phase 0 has a single idempotent
// migration matching SPEC.md's drop_points model.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlite database handle must not be nil")
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("migrate sqlite schema: %w", err)
	}
	return nil
}
