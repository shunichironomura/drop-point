package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shunichironomura/droppoint/internal/token"
)

// ErrAPITokenNotFound reports that no matching API token row exists.
var ErrAPITokenNotFound = errors.New("api token not found")

// APIToken describes an operator-managed API token row without exposing the
// stored secret hash.
type APIToken struct {
	ID                  string
	Enabled             bool
	MaxActiveDropPoints *int
	CreatedAt           time.Time
	DisabledAt          *time.Time
}

// AddAPITokenParams contains the stored fields for a newly generated API token.
type AddAPITokenParams struct {
	ID                  string
	SecretHash          string
	MaxActiveDropPoints *int
	CreatedAt           time.Time
}

// AddAPIToken inserts a new enabled API token row.
func (r *Repository) AddAPIToken(ctx context.Context, params AddAPITokenParams) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if err := validateAPITokenFields(params.ID, params.SecretHash, params.MaxActiveDropPoints); err != nil {
		return err
	}
	if params.CreatedAt.IsZero() {
		return fmt.Errorf("api token created_at must not be zero")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO api_tokens (id, secret_hash, enabled, max_active_drop_points, created_at, disabled_at)
VALUES (?, ?, 1, ?, ?, NULL)`,
		params.ID,
		params.SecretHash,
		nullInt(params.MaxActiveDropPoints),
		formatTime(params.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("add api token %q: %w", params.ID, err)
	}
	return nil
}

// FindEnabledAPITokenBySecretHash returns the enabled API token row with the
// supplied deterministic token hash.
func (r *Repository) FindEnabledAPITokenBySecretHash(ctx context.Context, secretHash string) (APIToken, error) {
	if err := r.ensureReady(); err != nil {
		return APIToken{}, err
	}
	if !token.ValidSHA256Hash(secretHash) {
		return APIToken{}, ErrAPITokenNotFound
	}
	row := r.db.QueryRowContext(ctx, `
SELECT id, enabled, max_active_drop_points, created_at, disabled_at
FROM api_tokens
WHERE secret_hash = ? AND enabled = 1`, secretHash)
	apiToken, err := scanAPIToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrAPITokenNotFound
	}
	if err != nil {
		return APIToken{}, err
	}
	return apiToken, nil
}

// ListAPITokens returns all API token rows ordered by operator ID.
func (r *Repository) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	if err := r.ensureReady(); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, enabled, max_active_drop_points, created_at, disabled_at
FROM api_tokens
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()

	var tokens []APIToken
	for rows.Next() {
		apiToken, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, apiToken)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan api tokens: %w", err)
	}
	return tokens, nil
}

// DisableAPIToken immediately prevents an API token from creating new drop
// points. Repeating it leaves the original disabled_at timestamp unchanged.
func (r *Repository) DisableAPIToken(ctx context.Context, id string, now time.Time) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("api token id must not be empty")
	}
	if now.IsZero() {
		return fmt.Errorf("api token disabled_at must not be zero")
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE api_tokens
SET enabled = 0, disabled_at = COALESCE(disabled_at, ?)
WHERE id = ?`, formatTime(now), id)
	if err != nil {
		return fmt.Errorf("disable api token %q: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("disable api token %q: rows affected: %w", id, err)
	}
	if changed == 0 {
		return ErrAPITokenNotFound
	}
	return nil
}

// RemoveAPIToken deletes an API token row. Existing drop points keep their
// api_token_id attribution string and are not modified.
func (r *Repository) RemoveAPIToken(ctx context.Context, id string) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("api token id must not be empty")
	}
	result, err := r.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("remove api token %q: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove api token %q: rows affected: %w", id, err)
	}
	if changed == 0 {
		return ErrAPITokenNotFound
	}
	return nil
}

func validateAPITokenFields(id string, secretHash string, maxActive *int) error {
	if id == "" {
		return fmt.Errorf("api token id must not be empty")
	}
	if !token.ValidSHA256Hash(secretHash) {
		return fmt.Errorf("api token %q secret_hash must use sha256:<lowercase-hex-sha256>", id)
	}
	if maxActive != nil && *maxActive <= 0 {
		return fmt.Errorf("api token %q max_active_drop_points must be positive when set", id)
	}
	return nil
}

func scanAPIToken(row scanner) (APIToken, error) {
	var (
		apiToken APIToken
		enabled  int
		max      sql.NullInt64
		created  string
		disabled sql.NullString
	)
	if err := row.Scan(&apiToken.ID, &enabled, &max, &created, &disabled); err != nil {
		return APIToken{}, err
	}
	apiToken.Enabled = enabled != 0
	if max.Valid {
		value := int(max.Int64)
		apiToken.MaxActiveDropPoints = &value
	}
	createdAt, err := parseTime(created)
	if err != nil {
		return APIToken{}, fmt.Errorf("parse api token %q created_at: %w", apiToken.ID, err)
	}
	apiToken.CreatedAt = createdAt
	apiToken.DisabledAt, err = parseNullTime(disabled)
	if err != nil {
		return APIToken{}, fmt.Errorf("parse api token %q disabled_at: %w", apiToken.ID, err)
	}
	return apiToken, nil
}

func nullInt(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}
