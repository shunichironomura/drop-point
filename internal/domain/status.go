package domain

// Status is the lifecycle state of a drop point (SPEC §5).
type Status string

const (
	// StatusOpen means the drop point exists and can accept exactly one drop.
	StatusOpen Status = "open"
	// StatusReceiving means the relay is receiving a drop stream.
	StatusReceiving Status = "receiving"
	// StatusReady means a durable encrypted payload is available for pickup.
	StatusReady Status = "ready"
	// StatusClosed means the receiver explicitly closed the drop point.
	StatusClosed Status = "closed"
	// StatusExpired means the TTL elapsed and the drop point is unusable.
	StatusExpired Status = "expired"
	// StatusFailed means a terminal internal failure needs cleanup.
	StatusFailed Status = "failed"
)

// Valid reports whether s is one of the defined status values.
func (s Status) Valid() bool {
	switch s {
	case StatusOpen, StatusReceiving, StatusReady, StatusClosed, StatusExpired, StatusFailed:
		return true
	default:
		return false
	}
}

// IsTerminal reports whether s is a terminal status with no outgoing
// transitions (SPEC §5).
func (s Status) IsTerminal() bool {
	switch s {
	case StatusClosed, StatusExpired, StatusFailed:
		return true
	default:
		return false
	}
}

// allowedTransitions maps each status to the set of statuses it may move to.
// Terminal statuses are absent (no outgoing edges), as are no-op self-edges.
// This table is the single source of truth for the SPEC §5 state machine; the
// SQLite repository guards every mutation with the same rules.
var allowedTransitions = map[Status]map[Status]bool{
	StatusOpen: {
		StatusReceiving: true, // a valid drop starts
		StatusClosed:    true, // receiver closes
		StatusExpired:   true, // TTL elapsed
		StatusFailed:    true, // terminal internal failure
	},
	StatusReceiving: {
		StatusReady:   true, // envelope+payload durably stored
		StatusOpen:    true, // failed/partial drop returns the single-use slot
		StatusClosed:  true,
		StatusExpired: true,
		StatusFailed:  true,
	},
	StatusReady: {
		StatusClosed:  true,
		StatusExpired: true,
		StatusFailed:  true,
	},
}

// CanTransition reports whether a drop point may move directly from status
// `from` to status `to` under the SPEC §5 state model. Transitions out of a
// terminal status, and no-op transitions to the same status, are not allowed.
func CanTransition(from, to Status) bool {
	return allowedTransitions[from][to]
}
