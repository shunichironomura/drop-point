package cleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
)

type BlobStore interface {
	DropPointIDs(ctx context.Context) ([]string, error)
	DeleteDropPoint(ctx context.Context, id string) error
	DeleteSubmission(ctx context.Context, dropPointID, submissionID string) error
}

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

func (s Service) ReconcileStartup(ctx context.Context) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	receiving, err := s.Repository.ReceivingSubmissions(ctx)
	if err != nil {
		return Result{}, err
	}
	recovered, err := s.recoverReceiving(ctx, receiving)
	if err != nil {
		return Result{RecoveredReceiving: recovered}, err
	}
	result, err := s.Expire(ctx)
	result.RecoveredReceiving += recovered
	return result, err
}

func (s Service) Expire(ctx context.Context) (Result, error) {
	if err := s.validate(); err != nil {
		return Result{}, err
	}
	now := s.now()
	result := Result{}
	if s.ReceivingStaleAfter > 0 {
		receiving, err := s.Repository.ReceivingSubmissionsStartedBefore(ctx, now.Add(-s.ReceivingStaleAfter))
		if err != nil {
			return result, err
		}
		recovered, err := s.recoverReceiving(ctx, receiving)
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

	children, err := s.Repository.CleanupSubmissions(ctx)
	if err != nil {
		return result, err
	}
	for _, submission := range children {
		if err := s.BlobStore.DeleteSubmission(ctx, submission.DropPointID, submission.ID); err != nil {
			return result, fmt.Errorf("delete terminal submission %q/%q blobs: %w", submission.DropPointID, submission.ID, err)
		}
		if err := s.Repository.ClearSubmissionFiles(ctx, submission.DropPointID, submission.ID); err != nil {
			return result, err
		}
		if submission.PayloadPath != "" || submission.EnvelopePath != "" {
			result.DeletedPayloads++
		}
	}

	terminal, err := s.Repository.TerminalDropPoints(ctx)
	if err != nil {
		return result, err
	}
	for _, dp := range terminal {
		files, err := s.Repository.SubmissionFilesForDropPoint(ctx, dp.ID)
		if err != nil {
			return result, err
		}
		if err := s.BlobStore.DeleteDropPoint(ctx, dp.ID); err != nil {
			return result, fmt.Errorf("delete terminal drop point %q blobs: %w", dp.ID, err)
		}
		if err := s.Repository.ClearDropPointFiles(ctx, dp.ID); err != nil {
			return result, err
		}
		result.DeletedPayloads += len(files)
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

func (s Service) recoverReceiving(ctx context.Context, receiving []droppoint.Submission) (int, error) {
	recovered := 0
	for _, submission := range receiving {
		if err := s.BlobStore.DeleteSubmission(ctx, submission.DropPointID, submission.ID); err != nil {
			return recovered, fmt.Errorf("delete interrupted submission %q/%q blobs: %w", submission.DropPointID, submission.ID, err)
		}
		if err := s.Repository.DeleteReceivingSubmission(ctx, submission.DropPointID, submission.ID); err != nil {
			return recovered, fmt.Errorf("delete interrupted submission %q/%q row: %w", submission.DropPointID, submission.ID, err)
		}
		recovered++
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
