package droppoint

import (
	"errors"
	"testing"
	"time"
)

func TestNewDropPoint(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp, err := New(CreateDropPointRequest{
		ID:              "dp_test",
		APITokenID:      "desktop-main",
		ClientName:      "client",
		DisplayName:     "calm-otter",
		DropTokenHash:   "sha256:drop",
		PickupTokenHash: "sha256:pick",
		TTL:             10 * time.Minute,
		MaxBytes:        1024,
	}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if dp.Status != StatusOpen {
		t.Fatalf("Status = %q, want open", dp.Status)
	}
	if !dp.ExpiresAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("ExpiresAt = %s", dp.ExpiresAt)
	}
}

func TestStatusTransitions(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := mustDropPoint(t, now)

	receiving, err := BeginReceiving(dp, now)
	if err != nil {
		t.Fatalf("BeginReceiving: %v", err)
	}
	if receiving.Status != StatusReceiving {
		t.Fatalf("Status = %q, want receiving", receiving.Status)
	}

	ready, err := CommitReceived(receiving, CommitDropResult{EnvelopePath: "envelope.json", PayloadPath: "payload.bin", EncryptedSize: 9}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("CommitReceived: %v", err)
	}
	if ready.Status != StatusReady || ready.DroppedAt == nil {
		t.Fatalf("ready transition did not record ready state: %+v", ready)
	}

	picked, err := MarkPickedUp(ready, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("MarkPickedUp: %v", err)
	}
	if picked.Status != StatusReady || picked.FirstPickedUpAt == nil {
		t.Fatalf("pickup should be non-terminal and record timestamp: %+v", picked)
	}

	pickedAgain, err := MarkPickedUp(picked, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("MarkPickedUp again: %v", err)
	}
	if !pickedAgain.FirstPickedUpAt.Equal(*picked.FirstPickedUpAt) {
		t.Fatalf("first pickup changed: %s -> %s", picked.FirstPickedUpAt, pickedAgain.FirstPickedUpAt)
	}

	closed, err := Close(pickedAgain, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.Status != StatusClosed || closed.ClosedAt == nil {
		t.Fatalf("close did not record terminal state: %+v", closed)
	}
	if _, err := Close(closed, now.Add(5*time.Second)); err != nil {
		t.Fatalf("Close should be idempotent: %v", err)
	}
}

func TestMarkPickedUpSurvivesTerminalRace(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for _, status := range []Status{StatusClosed, StatusExpired} {
		dp := mustDropPoint(t, now)
		dp.Status = status
		picked, err := MarkPickedUp(dp, now)
		if err != nil {
			t.Fatalf("MarkPickedUp(%s): %v", status, err)
		}
		if picked.Status != status || picked.FirstPickedUpAt == nil {
			t.Fatalf("MarkPickedUp(%s) = %+v", status, picked)
		}
	}
}

func TestRejectedTransitions(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := mustDropPoint(t, now)

	ready := dp
	ready.Status = StatusReady
	if _, err := BeginReceiving(ready, now); !errors.Is(err, ErrDropAlreadyExists) {
		t.Fatalf("BeginReceiving ready err = %v, want ErrDropAlreadyExists", err)
	}
	if _, err := CommitReceived(dp, CommitDropResult{EnvelopePath: "e", PayloadPath: "p"}, now); !errors.Is(err, ErrDropPointNotOpen) {
		t.Fatalf("CommitReceived open err = %v, want ErrDropPointNotOpen", err)
	}
	if _, err := MarkPickedUp(dp, now); !errors.Is(err, ErrDropPointNotOpen) {
		t.Fatalf("MarkPickedUp open err = %v, want ErrDropPointNotOpen", err)
	}

	expired := dp
	expired.ExpiresAt = now.Add(-time.Second)
	if _, err := BeginReceiving(expired, now); !errors.Is(err, ErrDropPointExpired) {
		t.Fatalf("BeginReceiving expired err = %v, want ErrDropPointExpired", err)
	}
	closed, err := Close(dp, now)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := BeginReceiving(closed, now); !errors.Is(err, ErrDropPointClosed) {
		t.Fatalf("BeginReceiving closed err = %v, want ErrDropPointClosed", err)
	}
}

func TestAbortReceivingReturnsToOpen(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := mustDropPoint(t, now)
	receiving, err := BeginReceiving(dp, now)
	if err != nil {
		t.Fatalf("BeginReceiving: %v", err)
	}
	open, err := AbortReceiving(receiving, now.Add(time.Second))
	if err != nil {
		t.Fatalf("AbortReceiving: %v", err)
	}
	if open.Status != StatusOpen {
		t.Fatalf("Status = %q, want open", open.Status)
	}
}

func TestFailRecordsTerminalTimestamp(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := mustDropPoint(t, now)
	failed, err := Fail(dp, now.Add(time.Second))
	if err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if failed.Status != StatusFailed || failed.FailedAt == nil || !failed.FailedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("failed drop point = %+v", failed)
	}
	failedAgain, err := Fail(failed, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("Fail retry: %v", err)
	}
	if !failedAgain.FailedAt.Equal(*failed.FailedAt) {
		t.Fatalf("failed_at changed: %v -> %v", failed.FailedAt, failedAgain.FailedAt)
	}
}

func TestExpireNonTerminal(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := mustDropPoint(t, now)
	expired, changed := Expire(dp, now.Add(11*time.Minute))
	if !changed || expired.Status != StatusExpired {
		t.Fatalf("Expire() = (%q, %v), want expired true", expired.Status, changed)
	}

	closed, err := Close(dp, now)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	stillClosed, changed := Expire(closed, now.Add(11*time.Minute))
	if changed || stillClosed.Status != StatusClosed {
		t.Fatalf("closed drop point should not expire: (%q, %v)", stillClosed.Status, changed)
	}
}

func mustDropPoint(t *testing.T, now time.Time) DropPoint {
	t.Helper()
	dp, err := New(CreateDropPointRequest{
		ID:              "dp_test",
		APITokenID:      "desktop-main",
		DisplayName:     "calm-otter",
		DropTokenHash:   "sha256:drop",
		PickupTokenHash: "sha256:pick",
		TTL:             10 * time.Minute,
		MaxBytes:        1024,
	}, now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return dp
}
