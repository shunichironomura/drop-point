package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRedactPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/drop/drop_secrettoken", "/drop/" + redactedMarker},
		{"/drop/drop_secrettoken/extra", "/drop/" + redactedMarker},
		{"/api/drops/drop_secrettoken", "/api/drops/" + redactedMarker},
		{"/health", "/health"},
		{"/api/drop-points/dp_public/status", "/api/drop-points/dp_public/status"},
		{"/", "/"},
	}
	for _, tt := range tests {
		if got := RedactPath(tt.in); got != tt.want {
			t.Errorf("RedactPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLogMiddlewareRedactsDropToken(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := logMiddleware(logger, ok)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/drop/drop_supersecret", nil)
	h.ServeHTTP(rec, req)

	logged := buf.String()
	if strings.Contains(logged, "supersecret") {
		t.Errorf("log leaked drop token: %s", logged)
	}
	if !strings.Contains(logged, "/drop/"+redactedMarker) {
		t.Errorf("log missing redacted path, got: %s", logged)
	}
}

func TestRecoverMiddlewareReturns500(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := recoverMiddleware(logger, panicky)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)

	// Must not propagate the panic.
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("body = %q, want generic internal_error", rec.Body.String())
	}
	// The panic must be logged but the panic value must not reach the client.
	if !strings.Contains(buf.String(), "panic_recovered") {
		t.Errorf("panic not logged: %s", buf.String())
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("panic value leaked to client: %s", rec.Body.String())
	}
}

func TestNewHandlesPanicEndToEnd(t *testing.T) {
	// A handler registered on the mux that panics should still yield a 500
	// through the full middleware chain rather than crashing.
	logger := discardLogger()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	h := logMiddleware(logger, recoverMiddleware(logger, mux))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
