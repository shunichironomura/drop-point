package cleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/shunichironomura/droppoint/internal/store"
)

type BlobStore interface {
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
	PurgedRows        int
}

// Expire marks expired non-terminal drop points and deletes their blob
// directories. It is safe to run repeatedly.
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
	for _, dp := range expired {
		if err := s.BlobStore.DeleteDropPoint(ctx, dp.ID); err != nil {
			return result, err
		}
		if err := s.Repository.DeleteDropPointFiles(ctx, dp.ID); err != nil {
			return result, err
		}
		if dp.PayloadPath != "" || dp.EnvelopePath != "" {
			result.DeletedPayloads++
		}
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
