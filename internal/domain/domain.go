// Package domain is the functional core of the DropPoint relay. It defines the
// drop point entity, the lifecycle state machine (SPEC §5), the typed inputs and
// results for repository operations, and the explicit error set those operations
// return.
//
// The domain is pure: it has no dependency on SQLite, HTTP, the filesystem, the
// clock, or logging. The imperative shell resolves public IDs, capability-token
// hashes, sizes, and timestamps and passes them in; the domain models token
// hashes only and never sees raw token secrets, plaintext filenames, encryption
// keys, or payload bytes (SPEC §6, §8, §14).
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Domain errors are explicit sentinels so callers branch with errors.Is instead
// of inspecting zero values. The HTTP shell maps these to status codes in later
// phases; several deliberately collapse to the same opaque client response so
// the relay does not reveal which drop points exist (SPEC §7.2, §7.4).
var (
	// ErrNotFound means no drop point matched the lookup key.
	ErrNotFound = errors.New("drop point not found")
	// ErrTokenMismatch means a capability token did not authorize the target
	// drop point.
	ErrTokenMismatch = errors.New("token does not authorize this drop point")
	// ErrExpired means the drop point's TTL has elapsed (SPEC §5).
	ErrExpired = errors.New("drop point has expired")
	// ErrClosed means the drop point was explicitly closed (SPEC §7.6).
	ErrClosed = errors.New("drop point is closed")
	// ErrFailed means the drop point is in the terminal failed state (SPEC §5).
	ErrFailed = errors.New("drop point is in a failed state")
	// ErrAlreadyDropped means the single-use slot is already taken by an
	// in-flight or committed drop (SPEC §7.3).
	ErrAlreadyDropped = errors.New("drop point already has a drop")
	// ErrNotReady means the drop point has no payload available for pickup
	// (SPEC §7.5).
	ErrNotReady = errors.New("drop point has no payload ready for pickup")
	// ErrInvalidTransition means a requested status change is not permitted by
	// the SPEC §5 state model.
	ErrInvalidTransition = errors.New("invalid status transition")
	// ErrInvalidParams means a repository request was missing required fields or
	// carried inconsistent values.
	ErrInvalidParams = errors.New("invalid drop point parameters")
)

// DropPoint is the relay's record of one temporary handoff session (SPEC §8). It
// holds operational metadata and capability-token hashes only.
type DropPoint struct {
	ID              string
	APITokenID      string
	ClientName      string // optional receiver label; empty when unset
	DropTokenHash   string
	PickupTokenHash string
	Status          Status

	// PayloadPath and EnvelopePath locate stored ciphertext once a drop has
	// committed; both are empty until then.
	PayloadPath  string
	EnvelopePath string

	// EncryptedSize is the stored payload length in bytes, set when a drop
	// commits and nil before then.
	EncryptedSize *int64

	MaxBytes int64

	CreatedAt       time.Time
	DroppedAt       *time.Time // durable drop completion; nil until ready
	FirstPickedUpAt *time.Time // first successful pickup; nil until picked up
	ClosedAt        *time.Time // explicit close; nil until closed
	ExpiresAt       time.Time
}

// ExpiredAt reports whether the drop point's TTL has elapsed as of now. A drop
// point exactly at its expiry instant is treated as expired (SPEC §5).
func (dp *DropPoint) ExpiredAt(now time.Time) bool {
	return !dp.ExpiresAt.After(now)
}

// CreateParams is the resolved input for persisting a new drop point. The shell
// generates the public ID and the drop/pickup tokens, hashes the tokens, and
// computes the timestamps before calling the repository.
type CreateParams struct {
	ID              string
	APITokenID      string
	ClientName      string // optional
	DropTokenHash   string
	PickupTokenHash string
	MaxBytes        int64
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

// Validate checks that the create parameters are internally consistent. It
// returns an error wrapping ErrInvalidParams on the first problem found.
func (p CreateParams) Validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("%w: id is empty", ErrInvalidParams)
	case p.APITokenID == "":
		return fmt.Errorf("%w: api_token_id is empty", ErrInvalidParams)
	case p.DropTokenHash == "":
		return fmt.Errorf("%w: drop_token_hash is empty", ErrInvalidParams)
	case p.PickupTokenHash == "":
		return fmt.Errorf("%w: pickup_token_hash is empty", ErrInvalidParams)
	case p.MaxBytes <= 0:
		return fmt.Errorf("%w: max_bytes must be positive, got %d", ErrInvalidParams, p.MaxBytes)
	case p.CreatedAt.IsZero():
		return fmt.Errorf("%w: created_at is zero", ErrInvalidParams)
	case p.ExpiresAt.IsZero():
		return fmt.Errorf("%w: expires_at is zero", ErrInvalidParams)
	case !p.ExpiresAt.After(p.CreatedAt):
		return fmt.Errorf("%w: expires_at must be after created_at", ErrInvalidParams)
	}
	return nil
}

// CommitParams is the resolved input for committing a received drop, moving a
// drop point from `receiving` to `ready` once its ciphertext is durably stored
// (SPEC §5, §7.3).
type CommitParams struct {
	ID            string
	PayloadPath   string
	EnvelopePath  string
	EncryptedSize int64
	DroppedAt     time.Time
}

// Validate checks that the commit parameters are internally consistent.
func (p CommitParams) Validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("%w: id is empty", ErrInvalidParams)
	case p.PayloadPath == "":
		return fmt.Errorf("%w: payload_path is empty", ErrInvalidParams)
	case p.EnvelopePath == "":
		return fmt.Errorf("%w: envelope_path is empty", ErrInvalidParams)
	case p.EncryptedSize < 0:
		return fmt.Errorf("%w: encrypted_size must not be negative, got %d", ErrInvalidParams, p.EncryptedSize)
	case p.DroppedAt.IsZero():
		return fmt.Errorf("%w: dropped_at is zero", ErrInvalidParams)
	}
	return nil
}
