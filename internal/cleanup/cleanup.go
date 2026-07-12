package cleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/shunichironomura/droppoint/internal/store"
)

type BlobStore interface {
	DropPointIDs(ctx context.Context) ([]string, error)
	DeleteDropPoint(ctx context.Context, id string) error
}

// Service expires old drop points and removes their ciphertext directories.
type Service struct {
	Repository        *store.Repository
	BlobStore         BlobStore
	Now               func() time.Time
	TerminalRetention time.Duration
}

type Result struct {
	ExpiredDropPoints int
	DeletedPayloads   int
	DeletedOrphans    int
	PurgedRows        int
}

// Expire marks elapsed rows expired, reconciles every terminal row with its
// blob directory, and removes directories that no repository row owns. Each
// step is safe to retry after interruption.
func (s Service) Expire(ctx context.Context) (Result, error) {
	if s.Repository == nil {
		return Result{}, fmt.Errorf("repository must not be nil")
	}
	if s.BlobStore == nil {
		return Result{}, fmt.Errorf("blob store must not be nil")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	expired, err := s.Repository.ExpireDropPoints(ctx, now)
	if err != nil {
		return Result{}, err
	}
	result := Result{ExpiredDropPoints: len(expired)}
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
