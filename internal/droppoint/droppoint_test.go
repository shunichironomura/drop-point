package droppoint

import (
	"errors"
	"testing"
	"time"
)

func TestNewDropPointCreatesReusableOpenSession(t *testing.T) {
	now := testNow()
	dp := mustDropPoint(t, now)
	if dp.Status != StatusOpen || dp.MaxPendingSubmissions != 10 || dp.MaxPendingBytes != 10_240 {
		t.Fatalf("new drop point = %+v", dp)
	}
	if !dp.ExpiresAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("expires_at = %s", dp.ExpiresAt)
	}
}

func TestSubmissionLifecycle(t *testing.T) {
	now := testNow()
	submission, err := NewSubmission(mustDropPoint(t, now), "sub_test", now)
	if err != nil {
		t.Fatalf("NewSubmission: %v", err)
	}
	if submission.Status != SubmissionStatusReceiving {
		t.Fatalf("status = %q", submission.Status)
	}

	ready, err := CommitSubmission(submission, CommitSubmissionResult{EnvelopePath: "envelope", PayloadPath: "payload", EncryptedSize: 9}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("CommitSubmission: %v", err)
	}
	if ready.Status != SubmissionStatusReady || ready.DroppedAt == nil {
		t.Fatalf("ready submission = %+v", ready)
	}

	picked, err := MarkSubmissionPickedUp(ready, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("MarkSubmissionPickedUp: %v", err)
	}
	first := *picked.FirstPickedUpAt
	picked, err = MarkSubmissionPickedUp(picked, now.Add(3*time.Second))
	if err != nil || !picked.FirstPickedUpAt.Equal(first) {
		t.Fatalf("idempotent pickup = %+v err=%v", picked, err)
	}

	acknowledged, err := AcknowledgeSubmission(picked, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("AcknowledgeSubmission: %v", err)
	}
	if acknowledged.Status != SubmissionStatusAcknowledged || acknowledged.AcknowledgedAt == nil {
		t.Fatalf("acknowledged submission = %+v", acknowledged)
	}
	if _, err := AcknowledgeSubmission(acknowledged, now.Add(5*time.Second)); err != nil {
		t.Fatalf("idempotent acknowledge: %v", err)
	}
}

func TestNewSubmissionRequiresOpenUnexpiredSession(t *testing.T) {
	now := testNow()
	for _, tt := range []struct {
		name   string
		status Status
		expiry time.Time
		want   error
	}{
		{name: "closed", status: StatusClosed, expiry: now.Add(time.Minute), want: ErrDropPointClosed},
		{name: "expired status", status: StatusExpired, expiry: now.Add(time.Minute), want: ErrDropPointExpired},
		{name: "elapsed", status: StatusOpen, expiry: now, want: ErrDropPointExpired},
		{name: "failed", status: StatusFailed, expiry: now.Add(time.Minute), want: ErrDropPointFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dp := mustDropPoint(t, now)
			dp.Status = tt.status
			dp.ExpiresAt = tt.expiry
			if _, err := NewSubmission(dp, "sub_test", now); !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFailedSubmissionDoesNotFailDropPoint(t *testing.T) {
	now := testNow()
	dp := mustDropPoint(t, now)
	submission, err := NewSubmission(dp, "sub_test", now)
	if err != nil {
		t.Fatal(err)
	}
	failed := FailSubmission(submission, now.Add(time.Second))
	if failed.Status != SubmissionStatusFailed || failed.FailedAt == nil {
		t.Fatalf("failed submission = %+v", failed)
	}
	if err := RequireOpen(dp, now.Add(2*time.Second)); err != nil {
		t.Fatalf("parent session was affected: %v", err)
	}
}

func TestCloseAndExpireSession(t *testing.T) {
	now := testNow()
	dp := mustDropPoint(t, now)
	elapsed, err := Close(dp, dp.ExpiresAt)
	if !errors.Is(err, ErrDropPointExpired) || elapsed.Status != StatusExpired {
		t.Fatalf("Close elapsed session = %+v, %v", elapsed, err)
	}

	closed, err := Close(dp, now)
	if err != nil || closed.Status != StatusClosed || closed.ClosedAt == nil {
		t.Fatalf("Close = %+v, %v", closed, err)
	}
	if _, err := Close(closed, now.Add(time.Second)); err != nil {
		t.Fatalf("Close retry: %v", err)
	}
	expired, changed := Expire(dp, now.Add(11*time.Minute))
	if !changed || expired.Status != StatusExpired {
		t.Fatalf("Expire = %+v, %v", expired, changed)
	}
}

func mustDropPoint(t *testing.T, now time.Time) DropPoint {
	t.Helper()
	dp, err := New(CreateDropPointRequest{
		ID:                    "dp_test",
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         "sha256:drop",
		PickupTokenHash:       "sha256:pick",
		TTL:                   10 * time.Minute,
		MaxBytes:              1024,
		MaxPendingSubmissions: 10,
		MaxPendingBytes:       10_240,
	}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return dp
}

func testNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}
