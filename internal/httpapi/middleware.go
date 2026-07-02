package httpapi

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shunichironomura/drop-point/internal/logutil"
)

// SetNoSniff applies a global MIME-sniffing opt-out to all routes.
func SetNoSniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// LogRequests emits one compact access log line per request.
func LogRequests(logger *log.Logger, next http.Handler) http.Handler {
	logger = logutil.DefaultLogger(logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		logger.Printf(
			"request method=%s path=%s status=%d bytes=%d duration=%s",
			r.Method,
			RedactTokenPath(r.URL.EscapedPath()),
			recorder.Status(),
			recorder.bytes,
			time.Since(start).Round(time.Microsecond),
		)
	})
}

// RecoverPanics converts panics into low-information HTTP 500 responses.
func RecoverPanics(logger *log.Logger, next http.Handler) http.Handler {
	logger = logutil.DefaultLogger(logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Printf("panic recovered method=%s path=%s error=%s", r.Method, RedactTokenPath(r.URL.EscapedPath()), fmt.Sprint(recovered))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// RedactTokenPath removes capability-token path values from log lines.
func RedactTokenPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/drop/"):
		return "/drop/:drop_token"
	case strings.HasPrefix(path, "/api/drops/"):
		return "/api/drops/:drop_token"
	default:
		return path
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(body)
	r.bytes += n
	return n, err
}
