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

// BlobStore is the filesystem payload boundary used by HTTP handlers.
type BlobStore interface {
	WriteDrop(ctx context.Context, id string, envelope []byte, payload io.Reader, maxBytes int64) (droppoint.CommitDropResult, error)
	ReadEnvelope(ctx context.Context, relative string) ([]byte, error)
	OpenPayload(ctx context.Context, relative string) (io.ReadCloser, error)
	DeleteDropPoint(ctx context.Context, id string) error
}

type dropPointStatusResponse struct {
	Status          droppoint.Status `json:"status"`
	DisplayName     string           `json:"display_name"`
	EncryptedSize   int64            `json:"encrypted_size"`
	DroppedAt       *time.Time       `json:"dropped_at"`
	FirstPickedUpAt *time.Time       `json:"first_picked_up_at"`
	ExpiresAt       time.Time        `json:"expires_at"`
}

// HandleGetDropPointStatus handles GET /api/drop-points/:drop_point_id/status.
func HandleGetDropPointStatus(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dp, ok := authorizePickup(w, r, deps, r.PathValue("drop_point_id"))
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, dropPointStatusResponse{
			Status:          dp.Status,
			DisplayName:     dp.DisplayName,
			EncryptedSize:   dp.EncryptedSize,
			DroppedAt:       dp.DroppedAt,
			FirstPickedUpAt: dp.FirstPickedUpAt,
			ExpiresAt:       dp.ExpiresAt,
		})
	}
}

// HandleCloseDropPoint handles DELETE /api/drop-points/:drop_point_id.
func HandleCloseDropPoint(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("drop_point_id")
		dp, ok := authorizePickup(w, r, deps, id)
		if !ok {
			return
		}
		if dp.Status == droppoint.StatusExpired {
			if err := deleteStoredDropFiles(r.Context(), deps, dp); err != nil {
				writeError(w, http.StatusInternalServerError, "drop_point_close_failed", "could not delete stored drop payload")
				return
			}
			writeError(w, http.StatusGone, "drop_point_expired", "drop point has expired")
			return
		}
		if dropPointHasFiles(dp) && deps.BlobStore == nil {
			writeError(w, http.StatusInternalServerError, "blob_store_unavailable", "payload storage is unavailable")
			return
		}
		if err := deps.Repository.CloseDropPoint(r.Context(), id, deps.Now().UTC()); err != nil {
			switch {
			case errors.Is(err, droppoint.ErrDropPointExpired):
				if err := deleteStoredDropFiles(r.Context(), deps, dp); err != nil {
					writeError(w, http.StatusInternalServerError, "drop_point_close_failed", "could not delete stored drop payload")
					return
				}
				writeError(w, http.StatusGone, "drop_point_expired", "drop point has expired")
			case errors.Is(err, droppoint.ErrDropPointNotFound), errors.Is(err, droppoint.ErrPickupTokenInvalid):
				writeError(w, http.StatusNotFound, "drop_point_not_found", "drop point not found")
			default:
				writeError(w, http.StatusInternalServerError, "drop_point_close_failed", "could not close drop point")
			}
			return
		}
		if err := deleteStoredDropFiles(r.Context(), deps, dp); err != nil {
			writeError(w, http.StatusInternalServerError, "drop_point_close_failed", "could not delete stored drop payload")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func dropPointHasFiles(dp *droppoint.DropPoint) bool {
	return dp != nil && (dp.PayloadPath != "" || dp.EnvelopePath != "")
}

func deleteStoredDropFiles(ctx context.Context, deps Dependencies, dp *droppoint.DropPoint) error {
	if !dropPointHasFiles(dp) {
		return nil
	}
	if deps.BlobStore == nil {
		return errors.New("payload storage is unavailable")
	}
	if err := deps.BlobStore.DeleteDropPoint(ctx, dp.ID); err != nil {
		return err
	}
	return deps.Repository.DeleteDropPointFiles(ctx, dp.ID)
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
