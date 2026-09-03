package httpapi

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/logutil"
	"github.com/shunichironomura/droppoint/internal/store"
)

// Repository is the lifecycle persistence boundary used by HTTP handlers.
type Repository interface {
	FindEnabledAPITokenBySecretHash(ctx context.Context, secretHash string) (store.APIToken, error)
	CreateDropPointWithinQuota(ctx context.Context, dp droppoint.DropPoint, maxActive int, now time.Time) error
	FindDropPointByID(ctx context.Context, id string) (*droppoint.DropPoint, error)
	FindOpenDropPointByDropTokenHash(ctx context.Context, dropTokenHash string, now time.Time) (*droppoint.DropPoint, error)
	BeginSubmission(ctx context.Context, dropPointID, submissionID string, now time.Time) error
	CommitSubmission(ctx context.Context, dropPointID, submissionID string, result droppoint.CommitSubmissionResult, now time.Time) error
	DeleteReceivingSubmission(ctx context.Context, dropPointID, submissionID string) error
	FindSubmission(ctx context.Context, dropPointID, submissionID string) (*droppoint.Submission, error)
	ListReadySubmissions(ctx context.Context, dropPointID string) ([]droppoint.Submission, error)
	PendingStats(ctx context.Context, dropPointID string) (store.PendingStats, error)
	MarkSubmissionPickedUp(ctx context.Context, dropPointID, submissionID string, now time.Time) error
	AcknowledgeSubmission(ctx context.Context, dropPointID, submissionID string, now time.Time) error
	FailSubmission(ctx context.Context, dropPointID, submissionID string, now time.Time) error
	ClearSubmissionFiles(ctx context.Context, dropPointID, submissionID string) error
	ClearDropPointFiles(ctx context.Context, id string) error
	FailDropPoint(ctx context.Context, id string, now time.Time) error
	AuthorizePickupToken(ctx context.Context, id string, pickupTokenHash string, now time.Time) (*droppoint.DropPoint, error)
	CloseDropPoint(ctx context.Context, id string, now time.Time) error
}

// Dependencies are the imperative-shell resources used by HTTP handlers.
type Dependencies struct {
	Config     config.Config
	Repository Repository
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
	mux.HandleFunc("GET /api/drop-points/{drop_point_id}/status", getOrHead(HandleGetDropPointStatus(deps)))
	mux.HandleFunc("/api/drop-points/{drop_point_id}/status", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("GET /api/drop-points/{drop_point_id}/submissions", getOrHead(HandleListSubmissions(deps)))
	mux.HandleFunc("/api/drop-points/{drop_point_id}/submissions", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("GET /api/drop-points/{drop_point_id}/submissions/{submission_id}/pickup", getOrHead(HandlePickupSubmission(deps)))
	mux.HandleFunc("/api/drop-points/{drop_point_id}/submissions/{submission_id}/pickup", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("DELETE /api/drop-points/{drop_point_id}/submissions/{submission_id}", HandleAcknowledgeSubmission(deps))
	mux.HandleFunc("/api/drop-points/{drop_point_id}/submissions/{submission_id}", methodNotAllowed("DELETE"))
	mux.HandleFunc("DELETE /api/drop-points/{drop_point_id}", HandleCloseDropPoint(deps))
	mux.HandleFunc("/api/drop-points/{drop_point_id}", methodNotAllowed("DELETE"))
	mux.HandleFunc("GET /api/drops/{drop_token}", getOrHead(HandleGetDropMetadata(deps)))
	mux.HandleFunc("/api/drops/{drop_token}", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("PUT /api/drops/{drop_token}/submissions/{submission_id}", HandleSubmitDrop(deps))
	mux.HandleFunc("/api/drops/{drop_token}/submissions/{submission_id}", methodNotAllowed("PUT"))
	mux.HandleFunc("GET /drop/{drop_token}", getOrHead(HandleServeDropPage))
	mux.HandleFunc("/drop/{drop_token}", methodNotAllowed("GET, HEAD"))
	mux.HandleFunc("GET /drop-assets/{asset}", getOrHead(HandleDropPageAsset))
	mux.HandleFunc("/drop-assets/{asset}", methodNotAllowed("GET, HEAD"))

	return RecoverPanics(logger, LogRequests(logger, SetNoSniff(ApplyCORS(deps.Config, mux))))
}

func getOrHead(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			handler(headResponseWriter{ResponseWriter: w}, r)
			return
		}
		handler(w, r)
	}
}

type headResponseWriter struct {
	http.ResponseWriter
}

func (w headResponseWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}
