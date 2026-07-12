package cleanup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
)

type BlobStore interface {
	DropPointIDs(ctx context.Context) ([]string, error)
	DeleteDropPoint(ctx context.Context, id string) error
}

// Service expires old drop points and removes their ciphertext directories.
type Service struct {
	Repository          *store.Repository
	BlobStore           BlobStore
	Now                 func() time.Time
	TerminalRetention   time.Duration
	ReceivingStaleAfter time.Duration
}

type Result struct {
	ExpiredDropPoints  int
	RecoveredReceiving int
	DeletedPayloads    int
	DeletedOrphans     int
	PurgedRows         int
}

// ReconcileStartup recovers every interrupted receiving attempt before the
// server accepts requests, then performs the regular terminal/orphan cleanup.
func (s Service) ReconcileStartup(ctx context.Context) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	now := s.now()
	receiving, err := s.Repository.ReceivingDropPoints(ctx)
	if err != nil {
		return Result{}, err
	}
	recovered, err := s.recoverReceiving(ctx, receiving, now)
	if err != nil {
		return Result{RecoveredReceiving: recovered}, err
	}
	result, err := s.Expire(ctx)
	result.RecoveredReceiving += recovered
	return result, err
}

// Expire recovers stale receiving attempts, marks elapsed rows expired,
// reconciles every terminal row with its blob directory, and removes
// directories that no repository row owns. Each step is safe to retry after
// interruption.
func (s Service) Expire(ctx context.Context) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	now := s.now()
	result := Result{}
	if s.ReceivingStaleAfter > 0 {
		receiving, err := s.Repository.ReceivingDropPointsStartedBefore(ctx, now.Add(-s.ReceivingStaleAfter))
		if err != nil {
			return result, err
		}
		recovered, err := s.recoverReceiving(ctx, receiving, now)
		result.RecoveredReceiving = recovered
		if err != nil {
			return result, err
		}
	}
	expired, err := s.Repository.ExpireDropPoints(ctx, now)
	if err != nil {
		return result, err
	}
	result.ExpiredDropPoints = len(expired)
	terminal, err := s.Repository.TerminalDropPoints(ctx)
	if err != nil {
		return result, err
	}
	for _, dp := range terminal {
		if err := s.BlobStore.DeleteDropPoint(ctx, dp.ID); err != nil {
			return result, fmt.Errorf("delete terminal drop point %q blobs: %w", dp.ID, err)
		}
		if err := s.Repository.DeleteDropPointFiles(ctx, dp.ID); err != nil {
			return result, err
		}
		if dp.PayloadPath != "" || dp.EnvelopePath != "" {
			result.DeletedPayloads++
		}
	}

	rowIDs, err := s.Repository.DropPointIDs(ctx)
	if err != nil {
		return result, err
	}
	blobIDs, err := s.BlobStore.DropPointIDs(ctx)
	if err != nil {
		return result, err
	}
	for _, id := range blobIDs {
		if _, exists := rowIDs[id]; exists {
			continue
		}
		if err := s.BlobStore.DeleteDropPoint(ctx, id); err != nil {
			return result, fmt.Errorf("delete orphan drop point %q blobs: %w", id, err)
		}
		result.DeletedOrphans++
	}

	if s.TerminalRetention > 0 {
		purged, err := s.Repository.PurgeTerminalDropPoints(ctx, now.Add(-s.TerminalRetention))
		if err != nil {
			return result, err
		}
		result.PurgedRows = purged
	}
	return result, nil
}

func (s Service) recoverReceiving(ctx context.Context, receiving []droppoint.DropPoint, now time.Time) (int, error) {
	recovered := 0
	for _, dp := range receiving {
		if err := s.BlobStore.DeleteDropPoint(ctx, dp.ID); err != nil {
			return recovered, fmt.Errorf("delete interrupted drop point %q blobs: %w", dp.ID, err)
		}
		err := s.Repository.ResetReceivingDrop(ctx, dp.ID, now)
		switch {
		case err == nil:
			recovered++
		case errors.Is(err, droppoint.ErrDropPointExpired):
			recovered++
		default:
			return recovered, fmt.Errorf("reset interrupted drop point %q: %w", dp.ID, err)
		}
	}
	return recovered, nil
}

func (s Service) validate() error {
	if s.Repository == nil {
		return fmt.Errorf("repository must not be nil")
	}
	if s.BlobStore == nil {
		return fmt.Errorf("blob store must not be nil")
	}
	return nil
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
