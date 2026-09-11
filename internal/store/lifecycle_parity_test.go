package store

import (
	"context"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
)

func TestRepositorySubmissionMutationsMatchDomainLifecycle(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	dp := testDropPoint(t, "dp_parity", "drop_parity", "pick_parity", now)
	insertTestDropPoint(t, repo, dp)
	if err := repo.BeginSubmission(context.Background(), dp.ID, testSubmissionOne, now); err != nil {
		t.Fatal(err)
	}
	stored := droppoint.CommitSubmissionResult{EnvelopePath: "e", PayloadPath: "p", EncryptedSize: 3}
	if err := repo.CommitSubmission(context.Background(), dp.ID, testSubmissionOne, stored, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	persisted, err := repo.FindSubmission(context.Background(), dp.ID, testSubmissionOne)
	if err != nil {
		t.Fatal(err)
	}
	domainReceiving, err := droppoint.NewSubmission(dp, testSubmissionOne, now)
	if err != nil {
		t.Fatal(err)
	}
	domainReady, err := droppoint.CommitSubmission(domainReceiving, stored, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domainReady.Status || persisted.EncryptedSize != domainReady.EncryptedSize || !persisted.DroppedAt.Equal(*domainReady.DroppedAt) {
		t.Fatalf("persisted=%+v domain=%+v", persisted, domainReady)
	}
}
