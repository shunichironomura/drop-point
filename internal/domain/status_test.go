package domain

import "testing"

func TestStatusValid(t *testing.T) {
	valid := []Status{StatusOpen, StatusReceiving, StatusReady, StatusClosed, StatusExpired, StatusFailed}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("Status(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []Status{"", "bogus", "OPEN", "ready "} {
		if s.Valid() {
			t.Errorf("Status(%q).Valid() = true, want false", s)
		}
	}
}

func TestStatusIsTerminal(t *testing.T) {
	terminal := map[Status]bool{
		StatusOpen:      false,
		StatusReceiving: false,
		StatusReady:     false,
		StatusClosed:    true,
		StatusExpired:   true,
		StatusFailed:    true,
	}
	for s, want := range terminal {
		if got := s.IsTerminal(); got != want {
			t.Errorf("Status(%q).IsTerminal() = %v, want %v", s, got, want)
		}
	}
}

// TestCanTransition exhaustively covers every from/to pair in the SPEC §5 state
// model so both allowed and rejected transitions are pinned down.
func TestCanTransition(t *testing.T) {
	all := []Status{StatusOpen, StatusReceiving, StatusReady, StatusClosed, StatusExpired, StatusFailed}

	// allowed[from] is the exact set of permitted destinations.
	allowed := map[Status]map[Status]bool{
		StatusOpen:      {StatusReceiving: true, StatusClosed: true, StatusExpired: true, StatusFailed: true},
		StatusReceiving: {StatusReady: true, StatusOpen: true, StatusClosed: true, StatusExpired: true, StatusFailed: true},
		StatusReady:     {StatusClosed: true, StatusExpired: true, StatusFailed: true},
		StatusClosed:    {},
		StatusExpired:   {},
		StatusFailed:    {},
	}

	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTerminalStatusesHaveNoOutgoingTransitions(t *testing.T) {
	all := []Status{StatusOpen, StatusReceiving, StatusReady, StatusClosed, StatusExpired, StatusFailed}
	for _, from := range []Status{StatusClosed, StatusExpired, StatusFailed} {
		for _, to := range all {
			if CanTransition(from, to) {
				t.Errorf("terminal status %q must not transition to %q", from, to)
			}
		}
	}
}
