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
  display_name TEXT NOT NULL DEFAULT '',
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
`

// Migrate applies the current SQLite schema and idempotent additive migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlite database handle must not be nil")
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("migrate sqlite schema: %w", err)
	}
	if err := ensureDropPointsColumn(ctx, db, "display_name", "ALTER TABLE drop_points ADD COLUMN display_name TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func ensureDropPointsColumn(ctx context.Context, db *sql.DB, name string, alterSQL string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(drop_points)")
	if err != nil {
		return fmt.Errorf("inspect drop_points columns: %w", err)
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
			return fmt.Errorf("scan drop_points column info: %w", err)
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect drop_points columns: %w", err)
	}

	if _, err := db.ExecContext(ctx, alterSQL); err != nil {
		return fmt.Errorf("add drop_points.%s column: %w", name, err)
	}
	return nil
}
