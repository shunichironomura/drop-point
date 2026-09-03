package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

type BlobStore interface {
	WriteSubmission(ctx context.Context, dropPointID, submissionID string, envelope []byte, payload io.Reader, maxBytes int64) (droppoint.CommitSubmissionResult, error)
	ReadEnvelope(ctx context.Context, relative string) ([]byte, error)
	OpenPayload(ctx context.Context, relative string) (io.ReadCloser, int64, error)
	DeleteSubmission(ctx context.Context, dropPointID, submissionID string) error
	DeleteDropPoint(ctx context.Context, id string) error
}

type dropPointStatusResponse struct {
	Status                droppoint.Status `json:"status"`
	DisplayName           string           `json:"display_name"`
	ExpiresAt             time.Time        `json:"expires_at"`
	PendingSubmissions    int              `json:"pending_submissions"`
	PendingBytes          int64            `json:"pending_bytes"`
	MaxPendingSubmissions int              `json:"max_pending_submissions"`
	MaxPendingBytes       int64            `json:"max_pending_bytes"`
}

type submissionSummary struct {
	SubmissionID    string     `json:"submission_id"`
	EncryptedSize   int64      `json:"encrypted_size"`
	DroppedAt       *time.Time `json:"dropped_at"`
	FirstPickedUpAt *time.Time `json:"first_picked_up_at"`
}

type listSubmissionsResponse struct {
	Submissions []submissionSummary `json:"submissions"`
}

func HandleGetDropPointStatus(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dp, ok := authorizePickup(w, r, deps, r.PathValue("drop_point_id"))
		if !ok {
			return
		}
		stats, err := deps.Repository.PendingStats(r.Context(), dp.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "drop_point_status_failed", "could not read drop point status")
			return
		}
		writeJSON(w, http.StatusOK, dropPointStatusResponse{
			Status:                dp.Status,
			DisplayName:           dp.DisplayName,
			ExpiresAt:             dp.ExpiresAt,
			PendingSubmissions:    stats.Submissions,
			PendingBytes:          stats.Bytes,
			MaxPendingSubmissions: dp.MaxPendingSubmissions,
			MaxPendingBytes:       dp.MaxPendingBytes,
		})
	}
}

func HandleListSubmissions(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dp, ok := authorizePickup(w, r, deps, r.PathValue("drop_point_id"))
		if !ok || !requireOpenDropPoint(w, dp) {
			return
		}
		submissions, err := deps.Repository.ListReadySubmissions(r.Context(), dp.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "submission_list_failed", "could not list submissions")
			return
		}
		response := listSubmissionsResponse{Submissions: make([]submissionSummary, 0, len(submissions))}
		for _, submission := range submissions {
			response.Submissions = append(response.Submissions, submissionSummary{
				SubmissionID:    submission.ID,
				EncryptedSize:   submission.EncryptedSize,
				DroppedAt:       submission.DroppedAt,
				FirstPickedUpAt: submission.FirstPickedUpAt,
			})
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func HandleAcknowledgeSubmission(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dp, ok := authorizePickup(w, r, deps, r.PathValue("drop_point_id"))
		if !ok || !requireOpenDropPoint(w, dp) {
			return
		}
		submissionID := r.PathValue("submission_id")
		if !token.ValidSubmissionID(submissionID) {
			writeError(w, http.StatusNotFound, "submission_not_found", "submission not found")
			return
		}
		if err := deps.Repository.AcknowledgeSubmission(r.Context(), dp.ID, submissionID, deps.Now().UTC()); err != nil {
			writeSubmissionUnavailable(w, err)
			return
		}
		if deps.BlobStore == nil {
			writeError(w, http.StatusInternalServerError, "blob_store_unavailable", "payload storage is unavailable")
			return
		}
		if err := deps.BlobStore.DeleteSubmission(r.Context(), dp.ID, submissionID); err != nil {
			writeError(w, http.StatusInternalServerError, "submission_acknowledge_failed", "could not delete acknowledged submission")
			return
		}
		if err := deps.Repository.ClearSubmissionFiles(r.Context(), dp.ID, submissionID); err != nil {
			writeError(w, http.StatusInternalServerError, "submission_acknowledge_failed", "could not finalize acknowledged submission")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func HandleCloseDropPoint(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("drop_point_id")
		dp, ok := authorizePickup(w, r, deps, id)
		if !ok {
			return
		}
		switch dp.Status {
		case droppoint.StatusOpen:
			if err := deps.Repository.CloseDropPoint(r.Context(), id, deps.Now().UTC()); err != nil {
				if errors.Is(err, droppoint.ErrDropPointExpired) {
					dp.Status = droppoint.StatusExpired
				} else {
					writeError(w, http.StatusInternalServerError, "drop_point_close_failed", "could not close drop point")
					return
				}
			}
		case droppoint.StatusClosed:
		case droppoint.StatusExpired, droppoint.StatusFailed:
		default:
			writeError(w, http.StatusConflict, "drop_point_unavailable", "drop point is unavailable")
			return
		}
		if err := deleteDropPointBlobs(r.Context(), deps, id); err != nil {
			writeError(w, http.StatusInternalServerError, "drop_point_close_failed", "could not delete stored submissions")
			return
		}
		if dp.Status == droppoint.StatusExpired || dp.Status == droppoint.StatusFailed {
			writeError(w, http.StatusGone, "drop_point_unavailable", "drop point is unavailable")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func deleteDropPointBlobs(ctx context.Context, deps Dependencies, id string) error {
	if deps.BlobStore == nil {
		return errors.New("payload storage is unavailable")
	}
	if err := deps.BlobStore.DeleteDropPoint(ctx, id); err != nil {
		return err
	}
	return deps.Repository.ClearDropPointFiles(ctx, id)
}

func requireOpenDropPoint(w http.ResponseWriter, dp *droppoint.DropPoint) bool {
	switch dp.Status {
	case droppoint.StatusOpen:
		return true
	case droppoint.StatusClosed, droppoint.StatusExpired, droppoint.StatusFailed:
		writeError(w, http.StatusGone, "drop_point_unavailable", "drop point is unavailable")
	default:
		writeError(w, http.StatusConflict, "drop_point_unavailable", "drop point is unavailable")
	}
	return false
}

func writeSubmissionUnavailable(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, droppoint.ErrSubmissionNotFound):
		writeError(w, http.StatusNotFound, "submission_not_found", "submission not found")
	case errors.Is(err, droppoint.ErrSubmissionAcknowledged):
		writeError(w, http.StatusGone, "submission_acknowledged", "submission is already acknowledged")
	case errors.Is(err, droppoint.ErrSubmissionFailed):
		writeError(w, http.StatusGone, "submission_failed", "submission is unavailable")
	case errors.Is(err, droppoint.ErrSubmissionNotReady):
		writeError(w, http.StatusConflict, "submission_not_ready", "submission is not ready")
	default:
		writeError(w, http.StatusInternalServerError, "submission_failed", "could not process submission")
	}
}

func authorizePickup(w http.ResponseWriter, r *http.Request, deps Dependencies, id string) (*droppoint.DropPoint, bool) {
	if deps.Repository == nil {
		writeError(w, http.StatusServiceUnavailable, "repository_unavailable", "drop point storage is unavailable")
		return nil, false
	}
	if id == "" {
		writeError(w, http.StatusNotFound, "drop_point_not_found", "drop point not found")
		return nil, false
	}
	pickupToken, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		writeError(w, http.StatusUnauthorized, "pickup_token_invalid", "valid bearer pickup token required")
		return nil, false
	}
	dp, err := deps.Repository.AuthorizePickupToken(r.Context(), id, token.HashSecret(pickupToken), deps.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, droppoint.ErrDropPointNotFound), errors.Is(err, droppoint.ErrPickupTokenInvalid):
			writeError(w, http.StatusNotFound, "drop_point_not_found", "drop point not found")
		default:
			writeError(w, http.StatusInternalServerError, "drop_point_lookup_failed", "could not look up drop point")
		}
		return nil, false
	}
	return dp, true
}
