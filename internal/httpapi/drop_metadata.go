package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

type dropMetadataResponse struct {
	DisplayName string    `json:"display_name"`
	ExpiresAt   time.Time `json:"expires_at"`
	MaxBytes    int64     `json:"max_bytes"`
}

// HandleGetDropMetadata handles GET /api/drops/:drop_token.
func HandleGetDropMetadata(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Repository == nil {
			writeError(w, http.StatusServiceUnavailable, "repository_unavailable", "drop point storage is unavailable")
			return
		}
		dropToken := r.PathValue("drop_token")
		if dropToken == "" {
			writeError(w, http.StatusNotFound, "drop_point_not_found", "drop point not found")
			return
		}

		dp, err := deps.Repository.FindOpenDropPointByDropTokenHash(r.Context(), token.HashSecret(dropToken), deps.Now().UTC())
		if err != nil {
			switch {
			case errors.Is(err, droppoint.ErrDropTokenInvalid):
				writeError(w, http.StatusNotFound, "drop_point_not_found", "drop point not found")
			case errors.Is(err, droppoint.ErrDropPointExpired):
				writeError(w, http.StatusGone, "drop_point_expired", "drop point has expired")
			case errors.Is(err, droppoint.ErrDropAlreadyExists), errors.Is(err, droppoint.ErrDropPointClosed), errors.Is(err, droppoint.ErrDropPointNotOpen):
				writeError(w, http.StatusConflict, "drop_point_unavailable", "drop point cannot accept files")
			default:
				writeError(w, http.StatusInternalServerError, "drop_point_lookup_failed", "could not look up drop point")
			}
			return
		}

		writeJSON(w, http.StatusOK, dropMetadataResponse{
			DisplayName: dp.DisplayName,
			ExpiresAt:   dp.ExpiresAt,
			MaxBytes:    dp.MaxBytes,
		})
	}
}
