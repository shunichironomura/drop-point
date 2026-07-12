package httpapi

import (
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/shunichironomura/droppoint/internal/logutil"
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
			requestLogPath(r),
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
				logger.Printf("panic recovered method=%s path=%s error_type=%T", r.Method, requestLogPath(r), recovered)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

var capabilityPathValuePattern = regexp.MustCompile(`(?:drop|pick|api)_[A-Za-z0-9_-]+`)

func requestLogPath(r *http.Request) string {
	if r != nil && r.Pattern != "" {
		pattern := r.Pattern
		if _, route, found := strings.Cut(pattern, " "); found {
			pattern = route
		}
		return strings.ReplaceAll(pattern, "{drop_token}", ":drop_token")
	}
	if r == nil || r.URL == nil {
		return "/:unknown"
	}
	return RedactTokenPath(r.URL.EscapedPath())
}

// RedactTokenPath removes capability-token values even when route delimiters
// or the capability itself use URL escaping. Malformed encodings are never
// returned verbatim when they resemble a token-bearing route.
func RedactTokenPath(path string) string {
	normalized := path
	for range 3 {
		decoded, err := url.PathUnescape(normalized)
		if err != nil || decoded == normalized {
			break
		}
		normalized = decoded
	}
	switch {
	case strings.HasPrefix(normalized, "/drop/"):
		return "/drop/:drop_token"
	case strings.HasPrefix(normalized, "/api/drops/"):
		return "/api/drops/:drop_token"
	}
	if redacted := capabilityPathValuePattern.ReplaceAllString(normalized, ":capability"); redacted != normalized {
		return redacted
	}
	if redacted := capabilityPathValuePattern.ReplaceAllString(path, ":capability"); redacted != path {
		return redacted
	}
	lowerPath := strings.ToLower(path)
	if strings.Contains(lowerPath, "%2f") && (strings.Contains(lowerPath, "drop") || strings.Contains(lowerPath, "%64%72%6f%70")) {
		return "/:redacted-capability-path"
	}
	return path
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
