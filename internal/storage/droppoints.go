package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shunichironomura/drop-point/internal/domain"
	"github.com/shunichironomura/drop-point/internal/token"
)

// dropPointColumns lists drop_points columns in the order scanDropPoint expects.
// SELECT and "... RETURNING" clauses share it so every read path uses one scanner.
const dropPointColumns = `id, api_token_id, client_name, drop_token_hash, pickup_token_hash, ` +
	`status, payload_path, envelope_path, encrypted_size, max_bytes, ` +
	`created_at, dropped_at, first_picked_up_at, closed_at, expires_at`

// activeStatusList is the SQL tuple of non-terminal statuses that occupy a drop
// point's single-use slot and count against an API token's active quota
// (SPEC §5, §7.1). database/sql cannot bind a list to a single placeholder, so
// the literal is concatenated into queries; it contains no external input.
const activeStatusList = `('open', 'receiving', 'ready')`

// rowScanner is satisfied by *sql.Row and *sql.Rows, letting scanDropPoint serve
// single-row queries, UPDATE ... RETURNING, and multi-row iteration alike.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDropPoint maps one drop_points row into a domain.DropPoint, translating
// nullable columns and Unix-second timestamps to their domain representations.
func scanDropPoint(sc rowScanner) (*domain.DropPoint, error) {
	var (
		dp            domain.DropPoint
		status        string
		clientName    sql.NullString
		payloadPath   sql.NullString
		envelopePath  sql.NullString
		encryptedSize sql.NullInt64
		createdAt     int64
		droppedAt     sql.NullInt64
		firstPickedUp sql.NullInt64
		closedAt      sql.NullInt64
		expiresAt     int64
	)
	if err := sc.Scan(
		&dp.ID, &dp.APITokenID, &clientName, &dp.DropTokenHash, &dp.PickupTokenHash,
		&status, &payloadPath, &envelopePath, &encryptedSize, &dp.MaxBytes,
		&createdAt, &droppedAt, &firstPickedUp, &closedAt, &expiresAt,
	); err != nil {
		return nil, err
	}
	dp.Status = domain.Status(status)
	dp.ClientName = clientName.String
	dp.PayloadPath = payloadPath.String
	dp.EnvelopePath = envelopePath.String
	if encryptedSize.Valid {
		v := encryptedSize.Int64
		dp.EncryptedSize = &v
	}
	dp.CreatedAt = unixToTime(createdAt)
	dp.ExpiresAt = unixToTime(expiresAt)
	dp.DroppedAt = nullableTime(droppedAt)
	dp.FirstPickedUpAt = nullableTime(firstPickedUp)
	dp.ClosedAt = nullableTime(closedAt)
	return &dp, nil
}

func unixToTime(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func nullableTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := unixToTime(n.Int64)
	return &t
}

// nullString maps "" to a SQL NULL so optional text columns stay NULL rather
// than empty strings.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// CreateDropPoint inserts a new drop point in the `open` state and returns the
// stored record. Only token hashes are written; raw tokens never reach a row
// (SPEC §8).
func (s *Store) CreateDropPoint(ctx context.Context, p domain.CreateParams) (*domain.DropPoint, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	const q = `INSERT INTO drop_points
		(id, api_token_id, client_name, drop_token_hash, pickup_token_hash,
		 status, max_bytes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?)
		RETURNING ` + dropPointColumns
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q,
		p.ID, p.APITokenID, nullString(p.ClientName), p.DropTokenHash, p.PickupTokenHash,
		p.MaxBytes, p.CreatedAt.Unix(), p.ExpiresAt.Unix()))
	if err != nil {
		return nil, fmt.Errorf("storage: create drop point: %w", err)
	}
	return dp, nil
}

// GetDropPoint returns the drop point with the given ID, or domain.ErrNotFound.
func (s *Store) GetDropPoint(ctx context.Context, id string) (*domain.DropPoint, error) {
	const q = `SELECT ` + dropPointColumns + ` FROM drop_points WHERE id = ?`
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q, id))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, domain.ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("storage: get drop point: %w", err)
	}
	return dp, nil
}

// FindOpenByDropTokenHash returns the open drop point whose drop token hashes to
// dropTokenHash, or domain.ErrNotFound. It is a read that does not mutate state
// or apply expiry; callers that claim the slot use BeginReceiving.
func (s *Store) FindOpenByDropTokenHash(ctx context.Context, dropTokenHash string) (*domain.DropPoint, error) {
	const q = `SELECT ` + dropPointColumns + ` FROM drop_points WHERE drop_token_hash = ? AND status = 'open'`
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q, dropTokenHash))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, domain.ErrNotFound
	case err != nil:
		return nil, fmt.Errorf("storage: find open drop point: %w", err)
	}
	return dp, nil
}

// AuthorizePickup loads the drop point with the given ID and verifies that
// pickupTokenHash matches its stored pickup token hash in constant time. It
// returns domain.ErrNotFound for an unknown ID and domain.ErrTokenMismatch when
// the token does not authorize this drop point, keeping the two failures
// distinct internally even though the HTTP shell may collapse them for clients.
func (s *Store) AuthorizePickup(ctx context.Context, id, pickupTokenHash string) (*domain.DropPoint, error) {
	dp, err := s.GetDropPoint(ctx, id)
	if err != nil {
		return nil, err
	}
	if !token.EqualHash(pickupTokenHash, dp.PickupTokenHash) {
		return nil, domain.ErrTokenMismatch
	}
	return dp, nil
}

// BeginReceiving atomically claims an open, unexpired drop point identified by
// its drop token hash, moving it to `receiving`. The single guarded UPDATE makes
// concurrent claims safe: at most one caller wins and the rest get a precise
// error (SPEC §5, §7.3).
func (s *Store) BeginReceiving(ctx context.Context, dropTokenHash string, now time.Time) (*domain.DropPoint, error) {
	const claim = `UPDATE drop_points SET status = 'receiving'
		WHERE drop_token_hash = ? AND status = 'open' AND expires_at > ?
		RETURNING ` + dropPointColumns
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, claim, dropTokenHash, now.Unix()))
	switch {
	case err == nil:
		return dp, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("storage: begin receiving: %w", err)
	}
	// The claim matched no row: diagnose why so the caller gets a precise error.
	return nil, s.diagnoseDropTokenClaim(ctx, dropTokenHash, now)
}

// CommitReceiving moves a `receiving` drop point to `ready`, recording the
// stored payload/envelope paths, encrypted size, and drop time. It is the only
// transition that consumes the single-use slot (SPEC §5).
func (s *Store) CommitReceiving(ctx context.Context, p domain.CommitParams) (*domain.DropPoint, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	const q = `UPDATE drop_points
		SET status = 'ready', payload_path = ?, envelope_path = ?, encrypted_size = ?, dropped_at = ?
		WHERE id = ? AND status = 'receiving'
		RETURNING ` + dropPointColumns
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q,
		p.PayloadPath, p.EnvelopePath, p.EncryptedSize, p.DroppedAt.Unix(), p.ID))
	switch {
	case err == nil:
		return dp, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("storage: commit receiving: %w", err)
	}
	return nil, s.diagnoseByID(ctx, p.ID, domain.StatusReceiving)
}

// AbortReceiving returns a `receiving` drop point to `open` after a failed or
// partial drop, preserving the single-use slot (SPEC §5). Phase 4 calls it on
// the drop endpoint's error path.
func (s *Store) AbortReceiving(ctx context.Context, id string) (*domain.DropPoint, error) {
	const q = `UPDATE drop_points SET status = 'open'
		WHERE id = ? AND status = 'receiving'
		RETURNING ` + dropPointColumns
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q, id))
	switch {
	case err == nil:
		return dp, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("storage: abort receiving: %w", err)
	}
	return nil, s.diagnoseByID(ctx, id, domain.StatusReceiving)
}

// RecordFirstPickup stamps first_picked_up_at on a ready, unexpired drop point
// if it is not already set, then returns the record. Pickup never changes status
// and is repeatable: a later call returns the existing timestamp unchanged
// (SPEC §5, §7.5).
func (s *Store) RecordFirstPickup(ctx context.Context, id string, now time.Time) (*domain.DropPoint, error) {
	const q = `UPDATE drop_points SET first_picked_up_at = ?
		WHERE id = ? AND status = 'ready' AND expires_at > ? AND first_picked_up_at IS NULL
		RETURNING ` + dropPointColumns
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q, now.Unix(), id, now.Unix()))
	switch {
	case err == nil:
		return dp, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("storage: record first pickup: %w", err)
	}
	// No row updated: the drop point may already be picked up (idempotent
	// success) or ineligible for pickup. Re-read to decide.
	dp, err = s.GetDropPoint(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := pickupEligibility(dp, now); err != nil {
		return nil, err
	}
	// Ready, unexpired, and already stamped: a repeated pickup.
	return dp, nil
}

// CloseDropPoint marks a non-terminal drop point `closed` and stamps closed_at.
// Close is idempotent: closing an already-closed drop point returns it unchanged
// (SPEC §7.6). Deleting stored ciphertext happens in the shell (Phase 3/5).
func (s *Store) CloseDropPoint(ctx context.Context, id string, now time.Time) (*domain.DropPoint, error) {
	const q = `UPDATE drop_points SET status = 'closed', closed_at = ?
		WHERE id = ? AND status IN ` + activeStatusList + `
		RETURNING ` + dropPointColumns
	dp, err := scanDropPoint(s.db.QueryRowContext(ctx, q, now.Unix(), id))
	switch {
	case err == nil:
		return dp, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("storage: close drop point: %w", err)
	}
	// No row updated: unknown ID, already closed (idempotent), or terminal.
	dp, err = s.GetDropPoint(ctx, id)
	if err != nil {
		return nil, err
	}
	if dp.Status == domain.StatusClosed {
		return dp, nil
	}
	return nil, statusError(dp.Status)
}

// ExpireDropPoints marks every non-terminal drop point whose TTL has elapsed as
// `expired` and returns their IDs so the shell can delete stored ciphertext
// (SPEC §5). It is idempotent: a second run finds nothing new.
func (s *Store) ExpireDropPoints(ctx context.Context, now time.Time) ([]string, error) {
	const q = `UPDATE drop_points SET status = 'expired'
		WHERE status IN ` + activeStatusList + ` AND expires_at <= ?
		RETURNING id`
	rows, err := s.db.QueryContext(ctx, q, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("storage: expire drop points: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: scan expired id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iterate expired ids: %w", err)
	}
	return ids, nil
}

// CountActiveByAPIToken returns the number of non-terminal drop points owned by
// apiTokenID, for active-quota enforcement at creation time (SPEC §7.1).
func (s *Store) CountActiveByAPIToken(ctx context.Context, apiTokenID string) (int, error) {
	const q = `SELECT COUNT(*) FROM drop_points
		WHERE api_token_id = ? AND status IN ` + activeStatusList
	var n int
	if err := s.db.QueryRowContext(ctx, q, apiTokenID).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count active drop points: %w", err)
	}
	return n, nil
}

// diagnoseDropTokenClaim explains why a BeginReceiving claim matched no row. It
// runs only on the failure path, so a slightly stale read here affects the error
// detail only, never the at-most-one-claim guarantee.
func (s *Store) diagnoseDropTokenClaim(ctx context.Context, dropTokenHash string, now time.Time) error {
	const q = `SELECT status, expires_at FROM drop_points WHERE drop_token_hash = ?`
	var status string
	var expiresAt int64
	err := s.db.QueryRowContext(ctx, q, dropTokenHash).Scan(&status, &expiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return domain.ErrNotFound
	case err != nil:
		return fmt.Errorf("storage: diagnose drop token: %w", err)
	}
	if domain.Status(status) == domain.StatusOpen {
		if expiresAt <= now.Unix() {
			return domain.ErrExpired
		}
		// Open and unexpired but the claim still failed: another caller won the
		// race between the UPDATE and this read. Report the slot as taken.
		return domain.ErrAlreadyDropped
	}
	return statusError(domain.Status(status))
}

// diagnoseByID explains why a mutation guarded on `expected` status matched no
// row for id: either the drop point is gone or it is in another status.
func (s *Store) diagnoseByID(ctx context.Context, id string, expected domain.Status) error {
	dp, err := s.GetDropPoint(ctx, id)
	if err != nil {
		return err // ErrNotFound or a wrapped DB error
	}
	if dp.Status == expected {
		// A re-read shows the expected status, so the row changed under us rather
		// than being in the wrong state. Surface a transition error, not success.
		return domain.ErrInvalidTransition
	}
	return statusError(dp.Status)
}

// statusError maps an observed status to the reason a slot claim or transition
// could not proceed from it.
func statusError(status domain.Status) error {
	switch status {
	case domain.StatusReceiving, domain.StatusReady:
		return domain.ErrAlreadyDropped
	case domain.StatusClosed:
		return domain.ErrClosed
	case domain.StatusExpired:
		return domain.ErrExpired
	case domain.StatusFailed:
		return domain.ErrFailed
	default: // including StatusOpen
		return domain.ErrInvalidTransition
	}
}

// pickupEligibility reports why a drop point cannot be picked up, or nil if it is
// ready and unexpired (SPEC §5, §7.5).
func pickupEligibility(dp *domain.DropPoint, now time.Time) error {
	if dp.Status != domain.StatusReady {
		switch dp.Status {
		case domain.StatusClosed:
			return domain.ErrClosed
		case domain.StatusExpired:
			return domain.ErrExpired
		case domain.StatusFailed:
			return domain.ErrFailed
		default: // open, receiving
			return domain.ErrNotReady
		}
	}
	if dp.ExpiredAt(now) {
		return domain.ErrExpired
	}
	return nil
}
