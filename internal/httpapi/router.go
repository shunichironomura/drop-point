package httpapi

import (
	"io"
	"log"
	"net/http"
)

// NewRouter builds the HTTP handler tree for the relay.
func NewRouter(logger *log.Logger) http.Handler {
	logger = defaultLogger(logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			HandleHealth(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		}
	})

	return RecoverPanics(logger, LogRequests(logger, mux))
}

func defaultLogger(logger *log.Logger) *log.Logger {
	if logger != nil {
		return logger
	}
	return log.New(io.Discard, "", 0)
}
