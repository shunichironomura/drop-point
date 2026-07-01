package httpapi

import (
	"log"
	"net/http"
	"time"

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/logutil"
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
	logger := logutil.DefaultLogger(deps.Logger)
	if deps.Now == nil {
		deps.Now = func() time.Time { return time.Now().UTC() }
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", HandleHealth)
	mux.HandleFunc("/health", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("POST /api/drop-points", HandleCreateDropPoint(deps))
	mux.HandleFunc("/api/drop-points", methodNotAllowed("POST"))
	mux.HandleFunc("GET /api/drop-points/{drop_point_id}/status", getOnly(HandleGetDropPointStatus(deps)))
	mux.HandleFunc("/api/drop-points/{drop_point_id}/status", methodNotAllowed("GET"))
	mux.HandleFunc("GET /api/drop-points/{drop_point_id}/pickup", getOnly(HandlePickupPayload(deps)))
	mux.HandleFunc("/api/drop-points/{drop_point_id}/pickup", methodNotAllowed("GET"))
	mux.HandleFunc("DELETE /api/drop-points/{drop_point_id}", HandleCloseDropPoint(deps))
	mux.HandleFunc("/api/drop-points/{drop_point_id}", methodNotAllowed("DELETE"))
	mux.HandleFunc("PUT /api/drops/{drop_token}", HandleSubmitDrop(deps))
	mux.HandleFunc("/api/drops/{drop_token}", methodNotAllowed("PUT"))
	mux.HandleFunc("GET /drop/{drop_token}", getOnly(HandleServeDropPage))
	mux.HandleFunc("/drop/{drop_token}", methodNotAllowed("GET"))
	mux.HandleFunc("GET /drop-assets/{asset}", getOnly(HandleDropPageAsset))
	mux.HandleFunc("/drop-assets/{asset}", methodNotAllowed("GET"))

	return RecoverPanics(logger, LogRequests(logger, SetNoSniff(ApplyCORS(deps.Config, mux))))
}

func getOnly(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			methodNotAllowed("GET")(w, r)
			return
		}
		handler(w, r)
	}
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
