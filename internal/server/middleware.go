package server

import (
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// redactedMarker replaces secret path segments in logs.
const redactedMarker = "<redacted>"

// RedactPath removes sender drop tokens embedded in URL paths so they never
// appear in logs (SPEC §15). Drop point IDs are public identifiers and are left
// intact; pickup and API tokens travel in the Authorization header, not the
// path, and are never logged here.
func RedactPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/drop/"):
		return "/drop/" + redactedMarker
	case strings.HasPrefix(path, "/api/drops/"):
		return "/api/drops/" + redactedMarker
	default:
		return path
	}
}

// statusRecorder captures the response status code and byte count for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// logMiddleware emits one structured access-log line per request. The path is
// redacted so token-bearing paths never reach the logs.
func logMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		logger.Info("http_request",
			slog.String("method", r.Method),
			slog.String("path", RedactPath(r.URL.Path)),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.bytes),
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// recoverMiddleware converts a panic in a downstream handler into a 500 response
// and a log entry, keeping the server alive. The recovered value is logged but
// never written to the client.
func recoverMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				logger.Error("panic_recovered",
					slog.String("method", r.Method),
					slog.String("path", RedactPath(r.URL.Path)),
					slog.Any("panic", v),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal_error"}` + "\n"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
