package httpapi

import (
	"io"
	"log"
	"net/http"
	"time"

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/store"
)

// Dependencies are the imperative-shell resources used by HTTP handlers.
type Dependencies struct {
	Config     config.Config
	Repository *store.Repository
	BlobStore  BlobStore
	Logger     *log.Logger
	Now        func() time.Time
}

// NewRouter builds the HTTP handler tree for tests that only need unauthenticated
// routes. Production code should use NewRouterWithDependencies.
func NewRouter(logger *log.Logger) http.Handler {
	return NewRouterWithDependencies(Dependencies{Config: config.Default(), Logger: logger})
}

// NewRouterWithDependencies builds the HTTP handler tree for the relay.
func NewRouterWithDependencies(deps Dependencies) http.Handler {
	logger := defaultLogger(deps.Logger)
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}

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
	mux.HandleFunc("/api/drop-points", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/drop-points" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost:
			HandleCreateDropPoint(deps)(w, r)
		default:
			w.Header().Set("Allow", "POST")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	})
	mux.HandleFunc("/api/drop-points/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && dropPointIDFromStatusPath(r.URL.Path) != "":
			HandleGetDropPointStatus(deps)(w, r)
		case r.Method == http.MethodGet && dropPointIDFromPickupPath(r.URL.Path) != "":
			HandlePickupPayload(deps)(w, r)
		case r.Method == http.MethodDelete && dropPointIDFromClosePath(r.URL.Path) != "":
			HandleCloseDropPoint(deps)(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/api/drops/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && dropTokenFromPath(r.URL.Path) != "":
			HandleSubmitDrop(deps)(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/drop/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		HandleServeDropPage(w, r)
	})
	mux.HandleFunc("/drop-assets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		HandleDropPageAsset(w, r)
	})

	return RecoverPanics(logger, LogRequests(logger, mux))
}

func defaultLogger(logger *log.Logger) *log.Logger {
	if logger != nil {
		return logger
	}
	return log.New(io.Discard, "", 0)
}
