package droppoint

import (
	"errors"
	"strings"
	"time"
)

type Status string

const (
	StatusOpen    Status = "open"
	StatusClosed  Status = "closed"
	StatusExpired Status = "expired"
	StatusFailed  Status = "failed"
)

type SubmissionStatus string

const (
	SubmissionStatusReceiving    SubmissionStatus = "receiving"
	SubmissionStatusReady        SubmissionStatus = "ready"
	SubmissionStatusAcknowledged SubmissionStatus = "acknowledged"
	SubmissionStatusFailed       SubmissionStatus = "failed"
)

var (
	ErrDropPointNotFound              = errors.New("drop point not found")
	ErrDropTokenInvalid               = errors.New("invalid drop token")
	ErrPickupTokenInvalid             = errors.New("invalid pickup token")
	ErrDropPointNotOpen               = errors.New("drop point is not open")
	ErrDropPointExpired               = errors.New("drop point expired")
	ErrDropPointClosed                = errors.New("drop point closed")
	ErrDropPointFailed                = errors.New("drop point failed")
	ErrSubmissionNotFound             = errors.New("submission not found")
	ErrSubmissionAlreadyExists        = errors.New("submission already exists")
	ErrSubmissionNotReady             = errors.New("submission is not ready")
	ErrSubmissionAcknowledged         = errors.New("submission acknowledged")
	ErrSubmissionFailed               = errors.New("submission failed")
	ErrPendingSubmissionQuotaExceeded = errors.New("pending submission quota exceeded")
	ErrPendingBytesQuotaExceeded      = errors.New("pending byte quota exceeded")
	ErrPayloadTooLarge                = errors.New("encrypted payload exceeds max_bytes")
	ErrEnvelopeInvalid                = errors.New("envelope invalid")
	ErrInvalidTransition              = errors.New("invalid drop point transition")
	ErrInvalidDropPoint               = errors.New("invalid drop point")
	ErrInvalidSubmission              = errors.New("invalid submission")
	ErrAPITokenInvalid                = errors.New("invalid api token")
	ErrAPITokenDisabled               = errors.New("api token disabled")
	ErrActiveDropPointQuotaExceeded   = errors.New("active drop point quota exceeded")
	ErrAPITokenNotFound               = errors.New("api token not found")
	ErrAPITokenHasActiveDropPoints    = errors.New("api token has active drop points")
)

type DropPoint struct {
	ID                    string
	APITokenID            string
	ClientName            string
	DisplayName           string
	DropTokenHash         string
	PickupTokenHash       string
	Status                Status
	CreatedAt             time.Time
	ClosedAt              *time.Time
	FailedAt              *time.Time
	ExpiresAt             time.Time
	MaxBytes              int64
	MaxPendingSubmissions int
	MaxPendingBytes       int64
}

type Submission struct {
	ID                 string
	DropPointID        string
	Status             SubmissionStatus
	EnvelopePath       string
	PayloadPath        string
	EncryptedSize      int64
	CreatedAt          time.Time
	ReceivingStartedAt time.Time
	DroppedAt          *time.Time
	FirstPickedUpAt    *time.Time
	AcknowledgedAt     *time.Time
	FailedAt           *time.Time
}

type CreateDropPointRequest struct {
	ID                    string
	APITokenID            string
	ClientName            string
	DisplayName           string
	DropTokenHash         string
	PickupTokenHash       string
	TTL                   time.Duration
	MaxBytes              int64
	MaxPendingSubmissions int
	MaxPendingBytes       int64
}

type CommitSubmissionResult struct {
	EnvelopePath  string
	PayloadPath   string
	EncryptedSize int64
}

type CreateDropPointResponse struct {
	DropPointID           string
	DisplayName           string
	DropToken             string
	PickupToken           string
	ExpiresAt             time.Time
	MaxBytes              int64
	MaxPendingSubmissions int
	MaxPendingBytes       int64
}

func New(req CreateDropPointRequest, now time.Time) (DropPoint, error) {
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.APITokenID) == "" || strings.TrimSpace(req.DisplayName) == "" || strings.TrimSpace(req.DropTokenHash) == "" || strings.TrimSpace(req.PickupTokenHash) == "" || req.TTL <= 0 || req.MaxBytes <= 0 || req.MaxPendingSubmissions <= 0 || req.MaxPendingBytes < req.MaxBytes {
		return DropPoint{}, ErrInvalidDropPoint
	}
	now = now.UTC()
	return DropPoint{
		ID:                    req.ID,
		APITokenID:            req.APITokenID,
		ClientName:            req.ClientName,
		DisplayName:           req.DisplayName,
		DropTokenHash:         req.DropTokenHash,
		PickupTokenHash:       req.PickupTokenHash,
		Status:                StatusOpen,
		CreatedAt:             now,
		ExpiresAt:             now.Add(req.TTL),
		MaxBytes:              req.MaxBytes,
		MaxPendingSubmissions: req.MaxPendingSubmissions,
		MaxPendingBytes:       req.MaxPendingBytes,
	}, nil
}

func NewSubmission(dropPoint DropPoint, id string, now time.Time) (Submission, error) {
	if strings.TrimSpace(id) == "" {
		return Submission{}, ErrInvalidSubmission
	}
	if err := RequireOpen(dropPoint, now); err != nil {
		return Submission{}, err
	}
	now = now.UTC()
	return Submission{
		ID:                 id,
		DropPointID:        dropPoint.ID,
		Status:             SubmissionStatusReceiving,
		CreatedAt:          now,
		ReceivingStartedAt: now,
	}, nil
}

func CommitSubmission(submission Submission, result CommitSubmissionResult, now time.Time) (Submission, error) {
	if submission.Status != SubmissionStatusReceiving || result.EnvelopePath == "" || result.PayloadPath == "" || result.EncryptedSize < 0 {
		return submission, ErrInvalidTransition
	}
	now = now.UTC()
	submission.Status = SubmissionStatusReady
	submission.EnvelopePath = result.EnvelopePath
	submission.PayloadPath = result.PayloadPath
	submission.EncryptedSize = result.EncryptedSize
	submission.DroppedAt = &now
	return submission, nil
}

func MarkSubmissionPickedUp(submission Submission, now time.Time) (Submission, error) {
	switch submission.Status {
	case SubmissionStatusReady, SubmissionStatusAcknowledged:
		if submission.FirstPickedUpAt == nil {
			now = now.UTC()
			submission.FirstPickedUpAt = &now
		}
		return submission, nil
	case SubmissionStatusFailed:
		return submission, ErrSubmissionFailed
	default:
		return submission, ErrSubmissionNotReady
	}
}

func AcknowledgeSubmission(submission Submission, now time.Time) (Submission, error) {
	switch submission.Status {
	case SubmissionStatusAcknowledged:
		return submission, nil
	case SubmissionStatusReady:
		now = now.UTC()
		submission.Status = SubmissionStatusAcknowledged
		submission.AcknowledgedAt = &now
		return submission, nil
	case SubmissionStatusFailed:
		return submission, ErrSubmissionFailed
	default:
		return submission, ErrSubmissionNotReady
	}
}

func FailSubmission(submission Submission, now time.Time) Submission {
	if submission.Status == SubmissionStatusFailed {
		return submission
	}
	now = now.UTC()
	submission.Status = SubmissionStatusFailed
	submission.FailedAt = &now
	return submission
}

func RequireOpen(dp DropPoint, now time.Time) error {
	if !now.Before(dp.ExpiresAt) && dp.Status == StatusOpen {
		return ErrDropPointExpired
	}
	switch dp.Status {
	case StatusOpen:
		return nil
	case StatusClosed:
		return ErrDropPointClosed
	case StatusExpired:
		return ErrDropPointExpired
	case StatusFailed:
		return ErrDropPointFailed
	default:
		return ErrDropPointNotOpen
	}
}

func Close(dp DropPoint, now time.Time) (DropPoint, error) {
	if expired, changed := Expire(dp, now); changed {
		return expired, ErrDropPointExpired
	}
	switch dp.Status {
	case StatusClosed:
		return dp, nil
	case StatusExpired:
		return dp, ErrDropPointExpired
	case StatusFailed:
		return dp, ErrDropPointFailed
	case StatusOpen:
		now = now.UTC()
		dp.Status = StatusClosed
		dp.ClosedAt = &now
		return dp, nil
	default:
		return dp, ErrInvalidTransition
	}
}

func Fail(dp DropPoint, now time.Time) (DropPoint, error) {
	if dp.Status == StatusFailed {
		return dp, nil
	}
	if dp.Status == StatusClosed || dp.Status == StatusExpired {
		return dp, ErrInvalidTransition
	}
	now = now.UTC()
	dp.Status = StatusFailed
	dp.FailedAt = &now
	return dp, nil
}

func Expire(dp DropPoint, now time.Time) (DropPoint, bool) {
	if dp.Status != StatusOpen || now.Before(dp.ExpiresAt) {
		return dp, false
	}
	dp.Status = StatusExpired
	return dp, true
}
