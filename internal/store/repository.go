package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const sqliteTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

const dropPointColumns = `id, api_token_id, client_name, display_name, drop_token_hash, pickup_token_hash,
status, created_at, closed_at, failed_at, expires_at, max_bytes, max_pending_submissions, max_pending_bytes`

const submissionColumns = `id, drop_point_id, status, envelope_path, payload_path, encrypted_size,
created_at, receiving_started_at, dropped_at, first_picked_up_at, acknowledged_at, failed_at`

type Repository struct {
	db *sql.DB
}

type PendingStats struct {
	Submissions int
	Bytes       int64
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateDropPointWithinQuota(ctx context.Context, dp droppoint.DropPoint, maxActive int, now time.Time) error {
	if err := r.ensureReady(); err != nil {
		return err
	}
	if maxActive <= 0 {
		return fmt.Errorf("max active drop points must be positive")
	}
	result, err := r.db.ExecContext(ctx, `
INSERT INTO drop_points (`+dropPointColumns+`)
SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
WHERE (
  SELECT count(*) FROM drop_points
  WHERE api_token_id = ? AND status = ? AND expires_at > ?
) < ?`,
		dp.ID, dp.APITokenID, nullString(dp.ClientName), dp.DisplayName, dp.DropTokenHash, dp.PickupTokenHash,
		string(dp.Status), formatTime(dp.CreatedAt), nullTime(dp.ClosedAt), nullTime(dp.FailedAt), formatTime(dp.ExpiresAt),
		dp.MaxBytes, dp.MaxPendingSubmissions, dp.MaxPendingBytes,
		dp.APITokenID, string(droppoint.StatusOpen), formatTime(now), maxActive,
	)
	if err != nil {
		return fmt.Errorf("create drop point %q within quota: %w", dp.ID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create drop point %q within quota: rows affected: %w", dp.ID, err)
	}
	if changed == 0 {
		return droppoint.ErrActiveDropPointQuotaExceeded
	}
	return nil
}

func (r *Repository) FindDropPointByID(ctx context.Context, id string) (*droppoint.DropPoint, error) {
	dp, err := scanDropPoint(r.db.QueryRowContext(ctx, `SELECT `+dropPointColumns+` FROM drop_points WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, droppoint.ErrDropPointNotFound
	}
	if err != nil {
		return nil, err
	}
	return dp, nil
}

func (r *Repository) FindDropPointByDropTokenHash(ctx context.Context, dropTokenHash string, now time.Time) (*droppoint.DropPoint, error) {
	dp, err := scanDropPoint(r.db.QueryRowContext(ctx, `SELECT `+dropPointColumns+` FROM drop_points WHERE drop_token_hash = ?`, dropTokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, droppoint.ErrDropTokenInvalid
	}
	if err != nil {
		return nil, err
	}
	if !now.Before(dp.ExpiresAt) && dp.Status == droppoint.StatusOpen {
		if err := r.markExpired(ctx, dp.ID); err != nil {
			return nil, err
		}
		dp.Status = droppoint.StatusExpired
	}
	return dp, nil
}

func (r *Repository) FindOpenDropPointByDropTokenHash(ctx context.Context, dropTokenHash string, now time.Time) (*droppoint.DropPoint, error) {
	dp, err := r.FindDropPointByDropTokenHash(ctx, dropTokenHash, now)
	if err != nil {
		return nil, err
	}
	if err := droppoint.RequireOpen(*dp, now); err != nil {
		return nil, err
	}
	return dp, nil
}

func (r *Repository) AuthorizePickupToken(ctx context.Context, id string, pickupTokenHash string, now time.Time) (*droppoint.DropPoint, error) {
	dp, err := r.FindDropPointByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !token.EqualHash(dp.PickupTokenHash, pickupTokenHash) {
		return nil, droppoint.ErrPickupTokenInvalid
	}
	if !now.Before(dp.ExpiresAt) && dp.Status == droppoint.StatusOpen {
		if err := r.markExpired(ctx, dp.ID); err != nil {
			return nil, err
		}
		dp.Status = droppoint.StatusExpired
	}
	return dp, nil
}

func (r *Repository) BeginSubmission(ctx context.Context, dropPointID, submissionID string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
INSERT INTO submissions (id, drop_point_id, status, created_at, receiving_started_at)
SELECT ?, id, ?, ?, ?
FROM drop_points
WHERE id = ? AND status = ? AND expires_at > ?
  AND (
    SELECT count(*) FROM submissions
    WHERE drop_point_id = ? AND status IN (?, ?)
  ) < max_pending_submissions`,
		submissionID, string(droppoint.SubmissionStatusReceiving), formatTime(now), formatTime(now),
		dropPointID, string(droppoint.StatusOpen), formatTime(now), dropPointID,
		string(droppoint.SubmissionStatusReceiving), string(droppoint.SubmissionStatusReady),
	)
	if IsUniqueConstraint(err) {
		return droppoint.ErrSubmissionAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("begin submission %q/%q: %w", dropPointID, submissionID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("begin submission %q/%q: rows affected: %w", dropPointID, submissionID, err)
	}
	if changed == 1 {
		return nil
	}
	dp, err := r.FindDropPointByID(ctx, dropPointID)
	if err != nil {
		return err
	}
	if err := droppoint.RequireOpen(*dp, now); err != nil {
		if errors.Is(err, droppoint.ErrDropPointExpired) {
			if markErr := r.markExpired(ctx, dropPointID); markErr != nil {
				return markErr
			}
		}
		return err
	}
	if _, err := r.FindSubmission(ctx, dropPointID, submissionID); err == nil {
		return droppoint.ErrSubmissionAlreadyExists
	} else if !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		return err
	}
	return droppoint.ErrPendingSubmissionQuotaExceeded
}

func (r *Repository) CommitSubmission(ctx context.Context, dropPointID, submissionID string, stored droppoint.CommitSubmissionResult, now time.Time) error {
	if stored.EnvelopePath == "" || stored.PayloadPath == "" || stored.EncryptedSize < 0 {
		return fmt.Errorf("commit submission %q/%q: invalid stored result", dropPointID, submissionID)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin commit submission transaction: %w", err)
	}
	defer tx.Rollback()

	dp, err := scanDropPoint(tx.QueryRowContext(ctx, `SELECT `+dropPointColumns+` FROM drop_points WHERE id = ?`, dropPointID))
	if errors.Is(err, sql.ErrNoRows) {
		return droppoint.ErrDropPointNotFound
	}
	if err != nil {
		return err
	}
	if err := droppoint.RequireOpen(*dp, now); err != nil {
		return err
	}
	submission, err := scanSubmission(tx.QueryRowContext(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE drop_point_id = ? AND id = ?`, dropPointID, submissionID))
	if errors.Is(err, sql.ErrNoRows) {
		return droppoint.ErrSubmissionNotFound
	}
	if err != nil {
		return err
	}
	if submission.Status != droppoint.SubmissionStatusReceiving {
		return submissionUnavailableError(submission.Status)
	}
	var pendingBytes int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(sum(encrypted_size), 0) FROM submissions
WHERE drop_point_id = ? AND status = ?`, dropPointID, string(droppoint.SubmissionStatusReady)).Scan(&pendingBytes); err != nil {
		return fmt.Errorf("sum pending submission bytes: %w", err)
	}
	if stored.EncryptedSize > dp.MaxPendingBytes-pendingBytes {
		return droppoint.ErrPendingBytesQuotaExceeded
	}
	result, err := tx.ExecContext(ctx, `
UPDATE submissions
SET status = ?, envelope_path = ?, payload_path = ?, encrypted_size = ?, dropped_at = ?
WHERE drop_point_id = ? AND id = ? AND status = ?`,
		string(droppoint.SubmissionStatusReady), stored.EnvelopePath, stored.PayloadPath, stored.EncryptedSize, formatTime(now),
		dropPointID, submissionID, string(droppoint.SubmissionStatusReceiving),
	)
	if err != nil {
		return fmt.Errorf("commit submission %q/%q: %w", dropPointID, submissionID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("commit submission %q/%q changed %d rows: %w", dropPointID, submissionID, changed, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit submission transaction: %w", err)
	}
	return nil
}

func (r *Repository) DeleteReceivingSubmission(ctx context.Context, dropPointID, submissionID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM submissions WHERE drop_point_id = ? AND id = ? AND status = ?`,
		dropPointID, submissionID, string(droppoint.SubmissionStatusReceiving))
	if err != nil {
		return fmt.Errorf("delete receiving submission %q/%q: %w", dropPointID, submissionID, err)
	}
	return nil
}

func (r *Repository) FindSubmission(ctx context.Context, dropPointID, submissionID string) (*droppoint.Submission, error) {
	submission, err := scanSubmission(r.db.QueryRowContext(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE drop_point_id = ? AND id = ?`, dropPointID, submissionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, droppoint.ErrSubmissionNotFound
	}
	if err != nil {
		return nil, err
	}
	return submission, nil
}

func (r *Repository) ListReadySubmissions(ctx context.Context, dropPointID string) ([]droppoint.Submission, error) {
	return r.querySubmissions(ctx, `
SELECT `+submissionColumns+` FROM submissions
WHERE drop_point_id = ? AND status = ?
ORDER BY dropped_at, id`, dropPointID, string(droppoint.SubmissionStatusReady))
}

func (r *Repository) PendingStats(ctx context.Context, dropPointID string) (PendingStats, error) {
	var stats PendingStats
	err := r.db.QueryRowContext(ctx, `
SELECT count(*), COALESCE(sum(CASE WHEN status = ? THEN encrypted_size ELSE 0 END), 0)
FROM submissions WHERE drop_point_id = ? AND status IN (?, ?)`,
		string(droppoint.SubmissionStatusReady), dropPointID,
		string(droppoint.SubmissionStatusReceiving), string(droppoint.SubmissionStatusReady),
	).Scan(&stats.Submissions, &stats.Bytes)
	if err != nil {
		return PendingStats{}, fmt.Errorf("read pending stats for %q: %w", dropPointID, err)
	}
	return stats, nil
}

func (r *Repository) MarkSubmissionPickedUp(ctx context.Context, dropPointID, submissionID string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE submissions SET first_picked_up_at = COALESCE(first_picked_up_at, ?)
WHERE drop_point_id = ? AND id = ? AND status IN (?, ?)`,
		formatTime(now), dropPointID, submissionID,
		string(droppoint.SubmissionStatusReady), string(droppoint.SubmissionStatusAcknowledged),
	)
	if err != nil {
		return fmt.Errorf("mark submission picked up %q/%q: %w", dropPointID, submissionID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	submission, findErr := r.FindSubmission(ctx, dropPointID, submissionID)
	if findErr != nil {
		return findErr
	}
	return submissionUnavailableError(submission.Status)
}

func (r *Repository) AcknowledgeSubmission(ctx context.Context, dropPointID, submissionID string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE submissions SET status = ?, acknowledged_at = COALESCE(acknowledged_at, ?)
WHERE drop_point_id = ? AND id = ? AND status = ?`,
		string(droppoint.SubmissionStatusAcknowledged), formatTime(now), dropPointID, submissionID, string(droppoint.SubmissionStatusReady),
	)
	if err != nil {
		return fmt.Errorf("acknowledge submission %q/%q: %w", dropPointID, submissionID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 1 {
		return nil
	}
	submission, findErr := r.FindSubmission(ctx, dropPointID, submissionID)
	if findErr != nil {
		return findErr
	}
	if submission.Status == droppoint.SubmissionStatusAcknowledged {
		return nil
	}
	return submissionUnavailableError(submission.Status)
}

func (r *Repository) FailSubmission(ctx context.Context, dropPointID, submissionID string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE submissions SET status = ?, failed_at = COALESCE(failed_at, ?)
WHERE drop_point_id = ? AND id = ? AND status IN (?, ?)`,
		string(droppoint.SubmissionStatusFailed), formatTime(now), dropPointID, submissionID,
		string(droppoint.SubmissionStatusReceiving), string(droppoint.SubmissionStatusReady),
	)
	if err != nil {
		return fmt.Errorf("fail submission %q/%q: %w", dropPointID, submissionID, err)
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 1 {
		return nil
	}
	submission, findErr := r.FindSubmission(ctx, dropPointID, submissionID)
	if findErr != nil {
		return findErr
	}
	if submission.Status == droppoint.SubmissionStatusFailed {
		return nil
	}
	return submissionUnavailableError(submission.Status)
}

func (r *Repository) ClearSubmissionFiles(ctx context.Context, dropPointID, submissionID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE submissions SET envelope_path = NULL, payload_path = NULL WHERE drop_point_id = ? AND id = ?`, dropPointID, submissionID)
	if err != nil {
		return fmt.Errorf("clear submission file pointers %q/%q: %w", dropPointID, submissionID, err)
	}
	return nil
}

func (r *Repository) ClearDropPointFiles(ctx context.Context, dropPointID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE submissions SET envelope_path = NULL, payload_path = NULL WHERE drop_point_id = ?`, dropPointID)
	if err != nil {
		return fmt.Errorf("clear drop point submission file pointers %q: %w", dropPointID, err)
	}
	return nil
}

func (r *Repository) CloseDropPoint(ctx context.Context, id string, now time.Time) error {
	dp, err := r.FindDropPointByID(ctx, id)
	if err != nil {
		return err
	}
	if dp.Status == droppoint.StatusClosed {
		return nil
	}
	if err := droppoint.RequireOpen(*dp, now); err != nil {
		if errors.Is(err, droppoint.ErrDropPointExpired) {
			if markErr := r.markExpired(ctx, id); markErr != nil {
				return markErr
			}
		}
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE drop_points SET status = ?, closed_at = ? WHERE id = ? AND status = ? AND expires_at > ?`,
		string(droppoint.StatusClosed), formatTime(now), id, string(droppoint.StatusOpen), formatTime(now))
	if err != nil {
		return fmt.Errorf("close drop point %q: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("close drop point %q: rows affected: %w", id, err)
	}
	if changed == 1 {
		return nil
	}
	dp, err = r.FindDropPointByID(ctx, id)
	if err != nil {
		return err
	}
	if dp.Status == droppoint.StatusClosed {
		return nil
	}
	if err := droppoint.RequireOpen(*dp, now); errors.Is(err, droppoint.ErrDropPointExpired) {
		if markErr := r.markExpired(ctx, id); markErr != nil {
			return markErr
		}
		return err
	} else if err != nil {
		return err
	}
	return droppoint.ErrDropPointNotOpen
}

func (r *Repository) FailDropPoint(ctx context.Context, id string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE drop_points SET status = ?, failed_at = COALESCE(failed_at, ?)
WHERE id = ? AND status = ?`, string(droppoint.StatusFailed), formatTime(now), id, string(droppoint.StatusOpen))
	if err != nil {
		return fmt.Errorf("fail drop point %q: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 1 {
		return nil
	}
	dp, findErr := r.FindDropPointByID(ctx, id)
	if findErr != nil {
		return findErr
	}
	if dp.Status == droppoint.StatusFailed {
		return nil
	}
	return unavailableDropPointError(dp.Status)
}

func (r *Repository) ExpireDropPoints(ctx context.Context, now time.Time) ([]droppoint.DropPoint, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+dropPointColumns+` FROM drop_points WHERE status = ? AND expires_at <= ? ORDER BY id`, string(droppoint.StatusOpen), formatTime(now))
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
		return nil, err
	}
	for _, dp := range expired {
		if err := r.markExpired(ctx, dp.ID); err != nil {
			return nil, err
		}
	}
	return expired, nil
}

func (r *Repository) ReceivingSubmissions(ctx context.Context) ([]droppoint.Submission, error) {
	return r.querySubmissions(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE status = ? ORDER BY drop_point_id, id`, string(droppoint.SubmissionStatusReceiving))
}

func (r *Repository) ReceivingSubmissionsStartedBefore(ctx context.Context, cutoff time.Time) ([]droppoint.Submission, error) {
	return r.querySubmissions(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE status = ? AND receiving_started_at <= ? ORDER BY drop_point_id, id`, string(droppoint.SubmissionStatusReceiving), formatTime(cutoff))
}

func (r *Repository) CleanupSubmissions(ctx context.Context) ([]droppoint.Submission, error) {
	return r.querySubmissions(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE status IN (?, ?) ORDER BY drop_point_id, id`, string(droppoint.SubmissionStatusAcknowledged), string(droppoint.SubmissionStatusFailed))
}

func (r *Repository) SubmissionFilesForDropPoint(ctx context.Context, dropPointID string) ([]droppoint.Submission, error) {
	return r.querySubmissions(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE drop_point_id = ? AND (envelope_path IS NOT NULL OR payload_path IS NOT NULL) ORDER BY id`, dropPointID)
}

func (r *Repository) TerminalDropPoints(ctx context.Context) ([]droppoint.DropPoint, error) {
	return r.queryDropPoints(ctx, `SELECT `+dropPointColumns+` FROM drop_points WHERE status IN (?, ?, ?) ORDER BY id`, string(droppoint.StatusClosed), string(droppoint.StatusExpired), string(droppoint.StatusFailed))
}

func (r *Repository) DropPointIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM drop_points`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func (r *Repository) PurgeTerminalDropPoints(ctx context.Context, cutoff time.Time) (int, error) {
	result, err := r.db.ExecContext(ctx, `
DELETE FROM drop_points
WHERE status IN (?, ?, ?)
  AND NOT EXISTS (
    SELECT 1 FROM submissions
    WHERE submissions.drop_point_id = drop_points.id
      AND (envelope_path IS NOT NULL OR payload_path IS NOT NULL)
  )
  AND (
    (status = ? AND closed_at IS NOT NULL AND closed_at <= ?)
    OR (status = ? AND expires_at <= ?)
    OR (status = ? AND failed_at IS NOT NULL AND failed_at <= ?)
  )`,
		string(droppoint.StatusClosed), string(droppoint.StatusExpired), string(droppoint.StatusFailed),
		string(droppoint.StatusClosed), formatTime(cutoff),
		string(droppoint.StatusExpired), formatTime(cutoff),
		string(droppoint.StatusFailed), formatTime(cutoff),
	)
	if err != nil {
		return 0, fmt.Errorf("purge terminal drop points: %w", err)
	}
	rows, err := result.RowsAffected()
	return int(rows), err
}

func (r *Repository) markExpired(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE drop_points SET status = ? WHERE id = ? AND status = ?`, string(droppoint.StatusExpired), id, string(droppoint.StatusOpen))
	if err != nil {
		return fmt.Errorf("mark drop point expired %q: %w", id, err)
	}
	return nil
}

func (r *Repository) queryDropPoints(ctx context.Context, query string, args ...any) ([]droppoint.DropPoint, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []droppoint.DropPoint
	for rows.Next() {
		dp, err := scanDropPoint(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *dp)
	}
	return result, rows.Err()
}

func (r *Repository) querySubmissions(ctx context.Context, query string, args ...any) ([]droppoint.Submission, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []droppoint.Submission
	for rows.Next() {
		submission, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *submission)
	}
	return result, rows.Err()
}

func unavailableDropPointError(status droppoint.Status) error {
	switch status {
	case droppoint.StatusClosed:
		return droppoint.ErrDropPointClosed
	case droppoint.StatusExpired:
		return droppoint.ErrDropPointExpired
	case droppoint.StatusFailed:
		return droppoint.ErrDropPointFailed
	default:
		return droppoint.ErrDropPointNotOpen
	}
}

func submissionUnavailableError(status droppoint.SubmissionStatus) error {
	switch status {
	case droppoint.SubmissionStatusAcknowledged:
		return droppoint.ErrSubmissionAcknowledged
	case droppoint.SubmissionStatusFailed:
		return droppoint.ErrSubmissionFailed
	case droppoint.SubmissionStatusReady:
		return droppoint.ErrSubmissionAlreadyExists
	default:
		return droppoint.ErrSubmissionNotReady
	}
}

func (r *Repository) ensureReady() error {
	if r == nil || r.db == nil {
		return fmt.Errorf("repository database handle must not be nil")
	}
	return nil
}

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
	var dp droppoint.DropPoint
	var clientName, closedAt, failedAt sql.NullString
	var status, createdAt, expiresAt string
	if err := row.Scan(
		&dp.ID, &dp.APITokenID, &clientName, &dp.DisplayName, &dp.DropTokenHash, &dp.PickupTokenHash,
		&status, &createdAt, &closedAt, &failedAt, &expiresAt, &dp.MaxBytes, &dp.MaxPendingSubmissions, &dp.MaxPendingBytes,
	); err != nil {
		return nil, err
	}
	dp.ClientName = clientName.String
	dp.Status = droppoint.Status(status)
	if dp.Status != droppoint.StatusOpen && dp.Status != droppoint.StatusClosed && dp.Status != droppoint.StatusExpired && dp.Status != droppoint.StatusFailed {
		return nil, fmt.Errorf("drop point %q has invalid status %q", dp.ID, status)
	}
	var err error
	if dp.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if dp.ClosedAt, err = parseNullTime(closedAt); err != nil {
		return nil, err
	}
	if dp.FailedAt, err = parseNullTime(failedAt); err != nil {
		return nil, err
	}
	if dp.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return nil, err
	}
	return &dp, nil
}

func scanSubmission(row scanner) (*droppoint.Submission, error) {
	var submission droppoint.Submission
	var status, createdAt, receivingStartedAt string
	var envelopePath, payloadPath, droppedAt, firstPickedUpAt, acknowledgedAt, failedAt sql.NullString
	var encryptedSize sql.NullInt64
	if err := row.Scan(
		&submission.ID, &submission.DropPointID, &status, &envelopePath, &payloadPath, &encryptedSize,
		&createdAt, &receivingStartedAt, &droppedAt, &firstPickedUpAt, &acknowledgedAt, &failedAt,
	); err != nil {
		return nil, err
	}
	submission.Status = droppoint.SubmissionStatus(status)
	switch submission.Status {
	case droppoint.SubmissionStatusReceiving, droppoint.SubmissionStatusReady, droppoint.SubmissionStatusAcknowledged, droppoint.SubmissionStatusFailed:
	default:
		return nil, fmt.Errorf("submission %q/%q has invalid status %q", submission.DropPointID, submission.ID, status)
	}
	submission.EnvelopePath = envelopePath.String
	submission.PayloadPath = payloadPath.String
	if encryptedSize.Valid {
		submission.EncryptedSize = encryptedSize.Int64
	}
	var err error
	if submission.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if submission.ReceivingStartedAt, err = parseTime(receivingStartedAt); err != nil {
		return nil, err
	}
	if submission.DroppedAt, err = parseNullTime(droppedAt); err != nil {
		return nil, err
	}
	if submission.FirstPickedUpAt, err = parseNullTime(firstPickedUpAt); err != nil {
		return nil, err
	}
	if submission.AcknowledgedAt, err = parseNullTime(acknowledgedAt); err != nil {
		return nil, err
	}
	if submission.FailedAt, err = parseNullTime(failedAt); err != nil {
		return nil, err
	}
	return &submission, nil
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
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
	return time.Parse(sqliteTimeFormat, value)
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
