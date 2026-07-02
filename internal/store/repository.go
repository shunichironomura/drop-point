package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/token"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// sqliteTimeFormat must stay fixed-width and all writes must use UTC so SQLite
// TEXT comparisons on expires_at preserve chronological ordering.
const sqliteTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

const insertDropPointSQL = `
INSERT INTO drop_points (
  id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash, status,
  payload_path, envelope_path, encrypted_size, created_at, dropped_at,
  first_picked_up_at, closed_at, expires_at, max_bytes
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const insertDropPointWithinQuotaSQL = `
INSERT INTO drop_points (
  id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash, status,
  payload_path, envelope_path, encrypted_size, created_at, dropped_at,
  first_picked_up_at, closed_at, expires_at, max_bytes
)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE (
  SELECT count(*)
  FROM drop_points
  WHERE api_token_id = ? AND status IN (?, ?, ?) AND expires_at > ?
) < ?`

// Repository provides typed persistence operations for drop point lifecycle rows.
type Repository struct {
	db *sql.DB
}

// NewRepository wraps db with typed drop point persistence methods.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// CreateDropPoint inserts a new drop point row. The supplied entity must contain
// token hashes only.
func (r *Repository) CreateDropPoint(ctx context.Context, dp droppoint.DropPoint) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if _, err := r.db.ExecContext(ctx, insertDropPointSQL, dropPointInsertArgs(dp)...); err != nil {
		return fmt.Errorf("create drop point %q: %w", dp.ID, err)
	}
	return nil
}

// CreateDropPointWithinQuota inserts a new drop point only if doing so keeps the
// API token's active open/receiving/ready drop point count below maxActive.
func (r *Repository) CreateDropPointWithinQuota(ctx context.Context, dp droppoint.DropPoint, maxActive int, now time.Time) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if maxActive <= 0 {
		return fmt.Errorf("max active drop points must be positive")
	}
	args := append(dropPointInsertArgs(dp),
		dp.APITokenID,
		string(droppoint.StatusOpen),
		string(droppoint.StatusReceiving),
		string(droppoint.StatusReady),
		formatTime(now),
		maxActive,
	)
	result, err := r.db.ExecContext(ctx, insertDropPointWithinQuotaSQL, args...)
	if err != nil {
		return fmt.Errorf("create drop point %q within quota: %w", dp.ID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create drop point %q within quota: rows affected: %w", dp.ID, err)
	}
	if changed == 1 {
		return nil
	}
	return droppoint.ErrActiveDropPointQuotaExceeded
}

// FindDropPointByID returns a drop point by public ID.
func (r *Repository) FindDropPointByID(ctx context.Context, id string) (*droppoint.DropPoint, error) {
	dp, err := r.queryOne(ctx, `
SELECT id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash, status,
       payload_path, envelope_path, encrypted_size, created_at, dropped_at,
       first_picked_up_at, closed_at, expires_at, max_bytes
FROM drop_points
WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, droppoint.ErrDropPointNotFound
	}
	if err != nil {
		return nil, err
	}
	return dp, nil
}

// FindDropPointByDropTokenHash authorizes a sender drop token hash and returns
// the matching drop point. Expired rows are marked expired before returning.
func (r *Repository) FindDropPointByDropTokenHash(ctx context.Context, dropTokenHash string, now time.Time) (*droppoint.DropPoint, error) {
	dp, err := r.queryOne(ctx, `
SELECT id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash, status,
       payload_path, envelope_path, encrypted_size, created_at, dropped_at,
       first_picked_up_at, closed_at, expires_at, max_bytes
FROM drop_points
WHERE drop_token_hash = ?`, dropTokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, droppoint.ErrDropTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if dp.IsExpiredAt(now) {
		_ = r.markExpired(ctx, dp.ID)
		dp.Status = droppoint.StatusExpired
	}
	return dp, nil
}

// FindOpenDropPointByDropTokenHash authorizes a sender drop token hash and
// returns the matching open drop point if it can receive a drop at now.
func (r *Repository) FindOpenDropPointByDropTokenHash(ctx context.Context, dropTokenHash string, now time.Time) (*droppoint.DropPoint, error) {
	dp, err := r.FindDropPointByDropTokenHash(ctx, dropTokenHash, now)
	if err != nil {
		return nil, err
	}
	if dp.Status == droppoint.StatusExpired {
		return nil, droppoint.ErrDropPointExpired
	}
	if dp.Status == droppoint.StatusOpen {
		return dp, nil
	}
	return nil, errorForUnavailableStatus(dp.Status)
}

// AuthorizePickupToken returns the drop point only when pickupTokenHash belongs
// to that exact drop point.
func (r *Repository) AuthorizePickupToken(ctx context.Context, id string, pickupTokenHash string, now time.Time) (*droppoint.DropPoint, error) {
	dp, err := r.FindDropPointByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !token.EqualHash(dp.PickupTokenHash, pickupTokenHash) {
		return nil, droppoint.ErrPickupTokenInvalid
	}
	if dp.IsExpiredAt(now) {
		_ = r.markExpired(ctx, dp.ID)
		dp.Status = droppoint.StatusExpired
		return dp, nil
	}
	return dp, nil
}

// BeginReceivingDrop atomically claims the single-use receiving slot.
func (r *Repository) BeginReceivingDrop(ctx context.Context, id string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE drop_points
SET status = ?
WHERE id = ? AND status = ? AND expires_at > ?`,
		string(droppoint.StatusReceiving), id, string(droppoint.StatusOpen), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("begin receiving drop %q: %w", id, err)
	}
	if changed, err := result.RowsAffected(); err == nil && changed == 1 {
		return nil
	}
	return r.classifyMutationMiss(ctx, id, now)
}

// CommitReceivedDrop records the durable envelope and payload and marks the drop
// point ready.
func (r *Repository) CommitReceivedDrop(ctx context.Context, id string, result droppoint.CommitDropResult, now time.Time) error {
	if result.EnvelopePath == "" || result.PayloadPath == "" {
		return fmt.Errorf("commit received drop %q: payload and envelope paths must be set", id)
	}
	if result.EncryptedSize < 0 {
		return fmt.Errorf("commit received drop %q: encrypted size must not be negative", id)
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE drop_points
SET status = ?, envelope_path = ?, payload_path = ?, encrypted_size = ?, dropped_at = ?
WHERE id = ? AND status = ? AND expires_at > ?`,
		string(droppoint.StatusReady), result.EnvelopePath, result.PayloadPath, result.EncryptedSize, formatTime(now),
		id, string(droppoint.StatusReceiving), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("commit received drop %q: %w", id, err)
	}
	if changed, err := res.RowsAffected(); err == nil && changed == 1 {
		return nil
	}
	return r.classifyMutationMiss(ctx, id, now)
}

// ResetReceivingDrop returns a failed in-flight drop to open unless it has
// expired by now.
func (r *Repository) ResetReceivingDrop(ctx context.Context, id string, now time.Time) error {
	dp, err := r.FindDropPointByID(ctx, id)
	if err != nil {
		return err
	}
	if dp.IsExpiredAt(now) {
		_ = r.markExpired(ctx, id)
		return droppoint.ErrDropPointExpired
	}
	if dp.Status != droppoint.StatusReceiving {
		return droppoint.ErrDropPointNotOpen
	}
	_, err = r.db.ExecContext(ctx, `
UPDATE drop_points
SET status = ?, payload_path = NULL, envelope_path = NULL, encrypted_size = NULL, dropped_at = NULL
WHERE id = ? AND status = ?`, string(droppoint.StatusOpen), id, string(droppoint.StatusReceiving))
	if err != nil {
		return fmt.Errorf("reset receiving drop %q: %w", id, err)
	}
	return nil
}

// MarkFirstPickedUp records first_picked_up_at after a successful pickup. It is
// safe to repeat and leaves the original timestamp unchanged.
func (r *Repository) MarkFirstPickedUp(ctx context.Context, id string, now time.Time) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE drop_points
SET first_picked_up_at = COALESCE(first_picked_up_at, ?)
WHERE id = ? AND status = ? AND expires_at > ?`,
		formatTime(now), id, string(droppoint.StatusReady), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("mark first pickup %q: %w", id, err)
	}
	if changed, err := res.RowsAffected(); err == nil && changed == 1 {
		return nil
	}
	return r.classifyMutationMiss(ctx, id, now)
}

// CloseDropPoint marks a drop point closed. Closing an already closed drop point
// is a no-op.
func (r *Repository) CloseDropPoint(ctx context.Context, id string, now time.Time) error {
	dp, err := r.FindDropPointByID(ctx, id)
	if err != nil {
		return err
	}
	if dp.Status == droppoint.StatusClosed {
		return nil
	}
	if dp.Status == droppoint.StatusExpired || dp.IsExpiredAt(now) {
		_ = r.markExpired(ctx, id)
		return droppoint.ErrDropPointExpired
	}
	if dp.Status == droppoint.StatusFailed {
		return droppoint.ErrDropPointNotOpen
	}
	res, err := r.db.ExecContext(ctx, `
UPDATE drop_points
SET status = ?, closed_at = ?
WHERE id = ? AND status IN (?, ?, ?) AND expires_at > ?`,
		string(droppoint.StatusClosed), formatTime(now), id,
		string(droppoint.StatusOpen), string(droppoint.StatusReceiving), string(droppoint.StatusReady), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("close drop point %q: %w", id, err)
	}
	if changed, err := res.RowsAffected(); err == nil && changed == 1 {
		return nil
	}
	err = r.classifyMutationMiss(ctx, id, now)
	if errors.Is(err, droppoint.ErrDropPointClosed) {
		return nil
	}
	return err
}

// ExpireDropPoints marks all expired non-terminal drop points expired and
// returns the affected rows for cleanup.
func (r *Repository) ExpireDropPoints(ctx context.Context, now time.Time) ([]droppoint.DropPoint, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash, status,
       payload_path, envelope_path, encrypted_size, created_at, dropped_at,
       first_picked_up_at, closed_at, expires_at, max_bytes
FROM drop_points
WHERE status IN (?, ?, ?) AND expires_at <= ?`,
		string(droppoint.StatusOpen), string(droppoint.StatusReceiving), string(droppoint.StatusReady), formatTime(now),
	)
	if err != nil {
		return nil, fmt.Errorf("select expired drop points: %w", err)
	}
	defer rows.Close()

	var expired []droppoint.DropPoint
	for rows.Next() {
		dp, err := scanDropPoint(rows)
		if err != nil {
			return nil, err
		}
		expired = append(expired, *dp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan expired drop points: %w", err)
	}

	for _, dp := range expired {
		if err := r.markExpired(ctx, dp.ID); err != nil {
			return nil, err
		}
	}
	return expired, nil
}

// PurgeTerminalDropPoints deletes terminal metadata rows whose ciphertext file
// pointers have already been cleared and whose terminal timestamp is older than
// cutoff. Closed rows use closed_at; expired and failed rows use expires_at.
func (r *Repository) PurgeTerminalDropPoints(ctx context.Context, cutoff time.Time) (int, error) {
	if err := r.ensureReady(); err != nil {
		return 0, err
	}
	result, err := r.db.ExecContext(ctx, `
DELETE FROM drop_points
WHERE status IN (?, ?, ?)
  AND payload_path IS NULL
  AND envelope_path IS NULL
  AND (
    (closed_at IS NOT NULL AND closed_at <= ?)
    OR (closed_at IS NULL AND expires_at <= ?)
  )`,
		string(droppoint.StatusClosed), string(droppoint.StatusExpired), string(droppoint.StatusFailed),
		formatTime(cutoff), formatTime(cutoff),
	)
	if err != nil {
		return 0, fmt.Errorf("purge terminal drop points: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge terminal drop points: rows affected: %w", err)
	}
	return int(rows), nil
}

// CountActiveDropPointsByAPITokenID counts open, receiving, and ready drop
// points that have not expired.
func (r *Repository) CountActiveDropPointsByAPITokenID(ctx context.Context, apiTokenID string, now time.Time) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `
SELECT count(*)
FROM drop_points
WHERE api_token_id = ? AND status IN (?, ?, ?) AND expires_at > ?`,
		apiTokenID,
		string(droppoint.StatusOpen), string(droppoint.StatusReceiving), string(droppoint.StatusReady),
		formatTime(now),
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active drop points for api token %q: %w", apiTokenID, err)
	}
	return count, nil
}

// DeleteDropPointFiles clears persisted file pointers after the imperative shell
// deletes the corresponding blob directory.
func (r *Repository) DeleteDropPointFiles(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE drop_points
SET payload_path = NULL, envelope_path = NULL, encrypted_size = NULL
WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("clear drop point file pointers %q: %w", id, err)
	}
	return nil
}

func (r *Repository) classifyMutationMiss(ctx context.Context, id string, now time.Time) error {
	dp, err := r.FindDropPointByID(ctx, id)
	if err != nil {
		return err
	}
	if dp.IsExpiredAt(now) {
		_ = r.markExpired(ctx, id)
		return droppoint.ErrDropPointExpired
	}
	return errorForUnavailableStatus(dp.Status)
}

func errorForUnavailableStatus(status droppoint.Status) error {
	switch status {
	case droppoint.StatusReady:
		return droppoint.ErrDropAlreadyExists
	case droppoint.StatusClosed:
		return droppoint.ErrDropPointClosed
	case droppoint.StatusExpired:
		return droppoint.ErrDropPointExpired
	default:
		return droppoint.ErrDropPointNotOpen
	}
}

func (r *Repository) markExpired(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `
UPDATE drop_points
SET status = ?
WHERE id = ? AND status IN (?, ?, ?)`,
		string(droppoint.StatusExpired), id,
		string(droppoint.StatusOpen), string(droppoint.StatusReceiving), string(droppoint.StatusReady),
	)
	if err != nil {
		return fmt.Errorf("mark drop point expired %q: %w", id, err)
	}
	return nil
}

func (r *Repository) queryOne(ctx context.Context, query string, args ...any) (*droppoint.DropPoint, error) {
	if err := r.ensureReady(); err != nil {
		return nil, err
	}
	return scanDropPoint(r.db.QueryRowContext(ctx, query, args...))
}

func (r *Repository) ensureReady() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("repository database handle must not be nil")
	}
	return nil
}

func dropPointInsertArgs(dp droppoint.DropPoint) []any {
	return []any{
		dp.ID,
		dp.APITokenID,
		nullString(dp.ClientName),
		dp.DisplayName,
		dp.DropTokenHash,
		dp.PickupTokenHash,
		string(dp.Status),
		nullString(dp.PayloadPath),
		nullString(dp.EnvelopePath),
		nullInt64(dp.EncryptedSize, dp.EncryptedSize > 0),
		formatTime(dp.CreatedAt),
		nullTime(dp.DroppedAt),
		nullTime(dp.FirstPickedUpAt),
		nullTime(dp.ClosedAt),
		formatTime(dp.ExpiresAt),
		dp.MaxBytes,
	}
}

// IsUniqueConstraint reports whether err wraps a SQLite primary-key or UNIQUE
// constraint failure. It intentionally checks driver result codes rather than
// localized or version-specific error text.
func IsUniqueConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() {
	case sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY, sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return true
	default:
		return false
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDropPoint(row scanner) (*droppoint.DropPoint, error) {
	var (
		dp              droppoint.DropPoint
		clientName      sql.NullString
		payloadPath     sql.NullString
		envelopePath    sql.NullString
		encryptedSize   sql.NullInt64
		createdAt       string
		droppedAt       sql.NullString
		firstPickedUpAt sql.NullString
		closedAt        sql.NullString
		expiresAt       string
		status          string
	)
	if err := row.Scan(
		&dp.ID,
		&dp.APITokenID,
		&clientName,
		&dp.DisplayName,
		&dp.DropTokenHash,
		&dp.PickupTokenHash,
		&status,
		&payloadPath,
		&envelopePath,
		&encryptedSize,
		&createdAt,
		&droppedAt,
		&firstPickedUpAt,
		&closedAt,
		&expiresAt,
		&dp.MaxBytes,
	); err != nil {
		return nil, err
	}
	dp.Status = droppoint.Status(status)
	if !dp.Status.Valid() {
		return nil, fmt.Errorf("drop point %q has invalid status %q", dp.ID, status)
	}
	dp.ClientName = clientName.String
	dp.PayloadPath = payloadPath.String
	dp.EnvelopePath = envelopePath.String
	if encryptedSize.Valid {
		dp.EncryptedSize = encryptedSize.Int64
	}
	var err error
	dp.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at for %q: %w", dp.ID, err)
	}
	dp.DroppedAt, err = parseNullTime(droppedAt)
	if err != nil {
		return nil, fmt.Errorf("parse dropped_at for %q: %w", dp.ID, err)
	}
	dp.FirstPickedUpAt, err = parseNullTime(firstPickedUpAt)
	if err != nil {
		return nil, fmt.Errorf("parse first_picked_up_at for %q: %w", dp.ID, err)
	}
	dp.ClosedAt, err = parseNullTime(closedAt)
	if err != nil {
		return nil, fmt.Errorf("parse closed_at for %q: %w", dp.ID, err)
	}
	dp.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return nil, fmt.Errorf("parse expires_at for %q: %w", dp.ID, err)
	}
	return &dp, nil
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullInt64(value int64, valid bool) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: valid}
}

func nullTime(value *time.Time) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*value), Valid: true}
}

func formatTime(value time.Time) string {
	return value.UTC().Format(sqliteTimeFormat)
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
