// Package server is the HTTP imperative shell for the DropPoint relay.
//
// Phase 0 wires a router with request logging, panic recovery, and an
// unauthenticated, low-information health check. Receiver and sender endpoints
// are added in later phases.
package server

import (
	"log/slog"
	"net/http"
)

// New builds the relay HTTP handler. Requests pass through request logging and
// panic-recovery middleware before reaching the route handlers. A nil logger
// falls back to slog.Default().
func New(logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)

	// Recovery is innermost so a panic is converted to a 500 before the logging
	// middleware records the request, yielding a single access-log line per
	// request (including panics) plus a separate panic log entry.
	return logMiddleware(logger, recoverMiddleware(logger, mux))
}

// handleHealth returns a minimal, unauthenticated liveness response. Per
// SPEC §7.7 it MUST NOT expose drop point counts, token material, file paths, or
// other operational detail.
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
}
