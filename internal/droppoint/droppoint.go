package droppoint

import (
	"errors"
	"fmt"
	"time"
)

// Status is the persisted drop point lifecycle state.
type Status string

const (
	StatusOpen      Status = "open"
	StatusReceiving Status = "receiving"
	StatusReady     Status = "ready"
	StatusClosed    Status = "closed"
	StatusExpired   Status = "expired"
	StatusFailed    Status = "failed"
)

var (
	ErrDropPointNotFound            = errors.New("drop point not found")
	ErrDropPointExpired             = errors.New("drop point expired")
	ErrDropPointClosed              = errors.New("drop point closed")
	ErrDropPointNotOpen             = errors.New("drop point is not open")
	ErrDropAlreadyExists            = errors.New("drop already exists")
	ErrActiveDropPointQuotaExceeded = errors.New("active drop point quota exceeded")
	ErrDropTokenInvalid             = errors.New("drop token invalid")
	ErrPickupTokenInvalid           = errors.New("pickup token invalid")
	ErrPayloadTooLarge              = errors.New("payload too large")
	ErrEnvelopeInvalid              = errors.New("envelope invalid")
)

// DropPoint is the domain entity. Token fields contain hashes only; raw
// capability tokens never belong in this structure.
type DropPoint struct {
	ID                 string
	APITokenID         string
	ClientName         string
	DisplayName        string
	DropTokenHash      string
	PickupTokenHash    string
	Status             Status
	PayloadPath        string
	EnvelopePath       string
	EncryptedSize      int64
	CreatedAt          time.Time
	DroppedAt          *time.Time
	ReceivingStartedAt *time.Time
	FirstPickedUpAt    *time.Time
	ClosedAt           *time.Time
	ExpiresAt          time.Time
	MaxBytes           int64
}

// CreateDropPointRequest contains validated token hashes and lifecycle limits
// used to construct a new drop point.
type CreateDropPointRequest struct {
	ID              string
	APITokenID      string
	ClientName      string
	DisplayName     string
	DropTokenHash   string
	PickupTokenHash string
	TTL             time.Duration
	MaxBytes        int64
}

// CreateDropPointResponse is the receiver-facing creation result. Raw tokens
// are included only in the response boundary, never in persisted rows.
type CreateDropPointResponse struct {
	DropPointID string
	DisplayName string
	DropToken   string
	PickupToken string
	ExpiresAt   time.Time
	MaxBytes    int64
}

// CommitDropResult is the durable storage result used to mark a drop ready.
type CommitDropResult struct {
	EnvelopePath  string
	PayloadPath   string
	EncryptedSize int64
}

// New constructs a new open drop point from validated inputs.
func New(req CreateDropPointRequest, now time.Time) (DropPoint, error) {
	if req.ID == "" {
		return DropPoint{}, fmt.Errorf("drop point id must not be empty")
	}
	if req.APITokenID == "" {
		return DropPoint{}, fmt.Errorf("api token id must not be empty")
	}
	if req.DisplayName == "" {
		return DropPoint{}, fmt.Errorf("display name must not be empty")
	}
	if req.DropTokenHash == "" || req.PickupTokenHash == "" {
		return DropPoint{}, fmt.Errorf("token hashes must not be empty")
	}
	if req.TTL <= 0 {
		return DropPoint{}, fmt.Errorf("ttl must be positive")
	}
	if req.MaxBytes <= 0 {
		return DropPoint{}, fmt.Errorf("max bytes must be positive")
	}
	now = now.UTC()
	return DropPoint{
		ID:              req.ID,
		APITokenID:      req.APITokenID,
		ClientName:      req.ClientName,
		DisplayName:     req.DisplayName,
		DropTokenHash:   req.DropTokenHash,
		PickupTokenHash: req.PickupTokenHash,
		Status:          StatusOpen,
		CreatedAt:       now,
		ExpiresAt:       now.Add(req.TTL),
		MaxBytes:        req.MaxBytes,
	}, nil
}

// Terminal reports whether the status cannot transition back to a usable state.
func (s Status) Terminal() bool {
	switch s {
	case StatusClosed, StatusExpired, StatusFailed:
		return true
	default:
		return false
	}
}

// Valid reports whether s is one of the known lifecycle statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusReceiving, StatusReady, StatusClosed, StatusExpired, StatusFailed:
		return true
	default:
		return false
	}
}

// IsExpiredAt reports whether the drop point must be treated as expired at now.
func (d DropPoint) IsExpiredAt(now time.Time) bool {
	return !d.Status.Terminal() && !d.ExpiresAt.After(now.UTC())
}

// BeginReceiving applies open -> receiving.
func BeginReceiving(d DropPoint, now time.Time) (DropPoint, error) {
	if expired, ok := expireIfElapsed(d, now); ok {
		return expired, ErrDropPointExpired
	}
	switch d.Status {
	case StatusOpen:
		d.Status = StatusReceiving
		startedAt := now.UTC()
		d.ReceivingStartedAt = &startedAt
		return d, nil
	case StatusClosed:
		return d, ErrDropPointClosed
	case StatusReady:
		return d, ErrDropAlreadyExists
	case StatusExpired:
		return d, ErrDropPointExpired
	default:
		return d, ErrDropPointNotOpen
	}
}

// CommitReceived applies receiving -> ready after durable writes succeed.
func CommitReceived(d DropPoint, result CommitDropResult, now time.Time) (DropPoint, error) {
	if expired, ok := expireIfElapsed(d, now); ok {
		return expired, ErrDropPointExpired
	}
	switch d.Status {
	case StatusReceiving:
	case StatusReady:
		return d, ErrDropAlreadyExists
	case StatusClosed:
		return d, ErrDropPointClosed
	case StatusExpired:
		return d, ErrDropPointExpired
	default:
		return d, ErrDropPointNotOpen
	}
	if result.PayloadPath == "" || result.EnvelopePath == "" {
		return d, fmt.Errorf("payload and envelope paths must be set")
	}
	if result.EncryptedSize < 0 {
		return d, fmt.Errorf("encrypted size must not be negative")
	}
	d.Status = StatusReady
	d.ReceivingStartedAt = nil
	d.PayloadPath = result.PayloadPath
	d.EnvelopePath = result.EnvelopePath
	d.EncryptedSize = result.EncryptedSize
	droppedAt := now.UTC()
	d.DroppedAt = &droppedAt
	return d, nil
}

// AbortReceiving returns receiving -> open after a failed or partial drop.
func AbortReceiving(d DropPoint, now time.Time) (DropPoint, error) {
	if expired, ok := expireIfElapsed(d, now); ok {
		return expired, ErrDropPointExpired
	}
	if d.Status != StatusReceiving {
		return d, ErrDropPointNotOpen
	}
	d.Status = StatusOpen
	d.ReceivingStartedAt = nil
	return d, nil
}

// MarkPickedUp records completion of a response that began while ready. Closed
// and expired are accepted because either transition may race after the final
// response write; the pickup event does not change lifecycle status.
func MarkPickedUp(d DropPoint, now time.Time) (DropPoint, error) {
	switch d.Status {
	case StatusReady, StatusClosed, StatusExpired:
		if d.FirstPickedUpAt == nil {
			pickedUpAt := now.UTC()
			d.FirstPickedUpAt = &pickedUpAt
		}
		return d, nil
	default:
		return d, ErrDropPointNotOpen
	}
}

// Close transitions an open, receiving, or ready drop point to closed. Closing an
// already closed drop point is safe and idempotent.
func Close(d DropPoint, now time.Time) (DropPoint, error) {
	if d.Status == StatusClosed {
		return d, nil
	}
	if expired, ok := expireIfElapsed(d, now); ok {
		return expired, ErrDropPointExpired
	}
	if d.Status == StatusExpired {
		return d, ErrDropPointExpired
	}
	if d.Status == StatusFailed {
		return d, ErrDropPointNotOpen
	}
	d.Status = StatusClosed
	d.ReceivingStartedAt = nil
	closedAt := now.UTC()
	d.ClosedAt = &closedAt
	return d, nil
}

// Expire transitions any non-terminal expired drop point to expired.
func Expire(d DropPoint, now time.Time) (DropPoint, bool) {
	return expireIfElapsed(d, now)
}

func expireIfElapsed(d DropPoint, now time.Time) (DropPoint, bool) {
	if !d.IsExpiredAt(now) {
		return d, false
	}
	d.Status = StatusExpired
	d.ReceivingStartedAt = nil
	return d, true
}
