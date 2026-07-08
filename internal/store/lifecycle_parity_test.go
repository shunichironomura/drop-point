package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
)

var lifecycleCommitResult = droppoint.CommitDropResult{
	EnvelopePath:  "drop-points/dp_lifecycle/new-envelope.json",
	PayloadPath:   "drop-points/dp_lifecycle/new-payload.bin",
	EncryptedSize: 42,
}

func TestRepositoryLifecycleMutationsMatchDomainTransitions(t *testing.T) {
	now := testNow()
	events := []struct {
		name  string
		repo  func(context.Context, *Repository, string, time.Time) error
		model func(droppoint.DropPoint, time.Time) (droppoint.DropPoint, error)
	}{
		{
			name: "begin_receiving",
			repo: func(ctx context.Context, repo *Repository, id string, now time.Time) error {
				return repo.BeginReceivingDrop(ctx, id, now)
			},
			model: droppoint.BeginReceiving,
		},
		{
			name: "commit_received",
			repo: func(ctx context.Context, repo *Repository, id string, now time.Time) error {
				return repo.CommitReceivedDrop(ctx, id, lifecycleCommitResult, now)
			},
			model: func(dp droppoint.DropPoint, now time.Time) (droppoint.DropPoint, error) {
				return droppoint.CommitReceived(dp, lifecycleCommitResult, now)
			},
		},
		{
			name: "reset_receiving",
			repo: func(ctx context.Context, repo *Repository, id string, now time.Time) error {
				return repo.ResetReceivingDrop(ctx, id, now)
			},
			model: droppoint.AbortReceiving,
		},
		{
			name: "mark_picked_up",
			repo: func(ctx context.Context, repo *Repository, id string, now time.Time) error {
				return repo.MarkFirstPickedUp(ctx, id, now)
			},
			model: droppoint.MarkPickedUp,
		},
		{
			name: "close",
			repo: func(ctx context.Context, repo *Repository, id string, now time.Time) error {
				return repo.CloseDropPoint(ctx, id, now)
			},
			model: droppoint.Close,
		},
	}

	for _, event := range events {
		for _, state := range lifecycleStates(now) {
			t.Run(event.name+"/"+state.name, func(t *testing.T) {
				repo := newTestRepository(t)
				dp := lifecycleDropPoint(t, fmt.Sprintf("dp_%s_%s", event.name, state.name), state.status, state.expiresAt, now)
				insertTestDropPoint(t, repo, dp)

				want, wantErr := event.model(dp, now)
				gotErr := event.repo(context.Background(), repo, dp.ID, now)
				assertMatchingDomainError(t, gotErr, wantErr)

				got, err := repo.FindDropPointByID(context.Background(), dp.ID)
				if err != nil {
					t.Fatalf("FindDropPointByID: %v", err)
				}
				assertSameLifecycleState(t, *got, want)
			})
		}
	}
}

func TestRepositoryExpireDropPointsMatchesDomainTransition(t *testing.T) {
	now := testNow()
	for _, state := range lifecycleStates(now) {
		t.Run(state.name, func(t *testing.T) {
			repo := newTestRepository(t)
			dp := lifecycleDropPoint(t, "dp_expire_"+state.name, state.status, state.expiresAt, now)
			insertTestDropPoint(t, repo, dp)

			want, wantChanged := droppoint.Expire(dp, now)
			affected, err := repo.ExpireDropPoints(context.Background(), now)
			if err != nil {
				t.Fatalf("ExpireDropPoints: %v", err)
			}
			if gotChanged := len(affected) == 1 && affected[0].ID == dp.ID; gotChanged != wantChanged {
				t.Fatalf("changed = %v, want %v; affected=%+v", gotChanged, wantChanged, affected)
			}

			got, err := repo.FindDropPointByID(context.Background(), dp.ID)
			if err != nil {
				t.Fatalf("FindDropPointByID: %v", err)
			}
			assertSameLifecycleState(t, *got, want)
		})
	}
}

type lifecycleState struct {
	name      string
	status    droppoint.Status
	expiresAt time.Time
}

func lifecycleStates(now time.Time) []lifecycleState {
	activeExpiry := now.Add(10 * time.Minute)
	expiredTime := now.Add(-time.Second)
	return []lifecycleState{
		{name: "open_active", status: droppoint.StatusOpen, expiresAt: activeExpiry},
		{name: "receiving_active", status: droppoint.StatusReceiving, expiresAt: activeExpiry},
		{name: "ready_active", status: droppoint.StatusReady, expiresAt: activeExpiry},
		{name: "closed_active", status: droppoint.StatusClosed, expiresAt: activeExpiry},
		{name: "expired_status", status: droppoint.StatusExpired, expiresAt: activeExpiry},
		{name: "failed_status", status: droppoint.StatusFailed, expiresAt: activeExpiry},
		{name: "open_expired", status: droppoint.StatusOpen, expiresAt: expiredTime},
		{name: "receiving_expired", status: droppoint.StatusReceiving, expiresAt: expiredTime},
		{name: "ready_expired", status: droppoint.StatusReady, expiresAt: expiredTime},
	}
}

func lifecycleDropPoint(t *testing.T, id string, status droppoint.Status, expiresAt time.Time, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp := testDropPoint(t, id, "drop_"+id, "pick_"+id, now)
	dp.Status = status
	dp.ExpiresAt = expiresAt
	switch status {
	case droppoint.StatusReady:
		droppedAt := now.Add(-time.Minute)
		dp.DroppedAt = &droppedAt
		dp.EnvelopePath = "drop-points/" + id + "/envelope.json"
		dp.PayloadPath = "drop-points/" + id + "/payload.bin"
		dp.EncryptedSize = 99
	case droppoint.StatusClosed:
		closedAt := now.Add(-time.Minute)
		dp.ClosedAt = &closedAt
	}
	return dp
}

func assertMatchingDomainError(t *testing.T, got error, want error) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("error = %v, want nil", got)
		}
		return
	}
	if got == nil {
		t.Fatalf("error = nil, want %v", want)
	}
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", got, want)
	}
}

func assertSameLifecycleState(t *testing.T, got droppoint.DropPoint, want droppoint.DropPoint) {
	t.Helper()
	if got.Status != want.Status {
		t.Fatalf("status = %q, want %q; got=%+v want=%+v", got.Status, want.Status, got, want)
	}
	if got.PayloadPath != want.PayloadPath || got.EnvelopePath != want.EnvelopePath || got.EncryptedSize != want.EncryptedSize {
		t.Fatalf("payload fields mismatch: got=%+v want=%+v", got, want)
	}
	assertTimePtrEqual(t, "dropped_at", got.DroppedAt, want.DroppedAt)
	assertTimePtrEqual(t, "first_picked_up_at", got.FirstPickedUpAt, want.FirstPickedUpAt)
	assertTimePtrEqual(t, "closed_at", got.ClosedAt, want.ClosedAt)
}

func assertTimePtrEqual(t *testing.T, name string, got *time.Time, want *time.Time) {
	t.Helper()
	switch {
	case got == nil && want == nil:
		return
	case got == nil || want == nil:
		t.Fatalf("%s = %v, want %v", name, got, want)
	case !got.Equal(*want):
		t.Fatalf("%s = %s, want %s", name, got, *want)
	}
}
