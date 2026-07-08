package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shunichironomura/droppoint/internal/dropname"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

const maxCreateRequestBytes = 1 << 20

type createDropPointRequest struct {
	ClientName string `json:"client_name"`
	TTLSeconds int    `json:"ttl_seconds"`
	MaxBytes   int64  `json:"max_bytes"`
	SingleUse  *bool  `json:"single_use"`
}

type createDropPointResponse struct {
	DropPointID string    `json:"drop_point_id"`
	DisplayName string    `json:"display_name"`
	DropLink    string    `json:"drop_link"`
	PickupToken string    `json:"pickup_token"`
	ExpiresAt   time.Time `json:"expires_at"`
	MaxBytes    int64     `json:"max_bytes"`
}

// HandleCreateDropPoint handles POST /api/drop-points.
func HandleCreateDropPoint(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Repository == nil {
			writeError(w, http.StatusServiceUnavailable, "repository_unavailable", "drop point storage is unavailable")
			return
		}
		authenticated, ok := authenticateAPIToken(deps.Config, r.Header.Get("Authorization"))
		if !ok {
			writeError(w, http.StatusUnauthorized, "api_token_invalid", "valid bearer API token required")
			return
		}

		var req createDropPointRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCreateRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}
		var trailing any
		switch err := decoder.Decode(&trailing); {
		case errors.Is(err, io.EOF):
		case err == nil:
			writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value")
			return
		default:
			writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
			return
		}

		ttlSeconds, maxBytes, ok := validateCreateRequest(w, deps, req)
		if !ok {
			return
		}

		now := deps.Now().UTC()
		created, err := createDropPoint(r.Context(), deps, authenticated.ID, authenticated.MaxActiveDropPoints, req.ClientName, time.Duration(ttlSeconds)*time.Second, maxBytes, now)
		if err != nil {
			if errors.Is(err, droppoint.ErrActiveDropPointQuotaExceeded) {
				writeError(w, http.StatusTooManyRequests, "active_drop_point_quota_exceeded", "active drop point quota exceeded")
				return
			}
			writeError(w, http.StatusInternalServerError, "drop_point_create_failed", "could not create drop point")
			return
		}

		writeJSON(w, http.StatusCreated, createDropPointResponse{
			DropPointID: created.DropPointID,
			DisplayName: created.DisplayName,
			DropLink:    dropLink(deps.Config.BaseURL, created.DropToken),
			PickupToken: created.PickupToken,
			ExpiresAt:   created.ExpiresAt,
			MaxBytes:    created.MaxBytes,
		})
	}
}

func validateCreateRequest(w http.ResponseWriter, deps Dependencies, req createDropPointRequest) (int, int64, bool) {
	if req.SingleUse != nil && !*req.SingleUse {
		writeError(w, http.StatusBadRequest, "single_use_required", "drop points are always single-use")
		return 0, 0, false
	}
	ttlSeconds := req.TTLSeconds
	if ttlSeconds == 0 {
		ttlSeconds = deps.Config.DefaultTTLSeconds
	}
	if ttlSeconds <= 0 {
		writeError(w, http.StatusBadRequest, "ttl_seconds_invalid", "ttl_seconds must be positive")
		return 0, 0, false
	}
	if ttlSeconds > deps.Config.MaxTTLSeconds {
		writeError(w, http.StatusBadRequest, "ttl_seconds_too_large", "ttl_seconds exceeds the configured maximum")
		return 0, 0, false
	}
	maxBytes := req.MaxBytes
	if maxBytes == 0 {
		maxBytes = deps.Config.DefaultMaxBytes
	}
	if maxBytes <= 0 {
		writeError(w, http.StatusBadRequest, "max_bytes_invalid", "max_bytes must be positive")
		return 0, 0, false
	}
	if maxBytes > deps.Config.MaxBytes {
		writeError(w, http.StatusBadRequest, "max_bytes_too_large", "max_bytes exceeds the configured maximum")
		return 0, 0, false
	}
	return ttlSeconds, maxBytes, true
}

func createDropPoint(ctx context.Context, deps Dependencies, apiTokenID string, maxActive int, clientName string, ttl time.Duration, maxBytes int64, now time.Time) (droppoint.CreateDropPointResponse, error) {
	for attempts := 0; attempts < 3; attempts++ {
		id, dropToken, pickupToken, err := generateDropPointSecrets()
		if err != nil {
			return droppoint.CreateDropPointResponse{}, err
		}
		displayName, err := dropname.Generate()
		if err != nil {
			return droppoint.CreateDropPointResponse{}, err
		}
		dp, err := droppoint.New(droppoint.CreateDropPointRequest{
			ID:              id,
			APITokenID:      apiTokenID,
			ClientName:      clientName,
			DisplayName:     displayName,
			DropTokenHash:   token.HashSecret(dropToken),
			PickupTokenHash: token.HashSecret(pickupToken),
			TTL:             ttl,
			MaxBytes:        maxBytes,
		}, now)
		if err != nil {
			return droppoint.CreateDropPointResponse{}, err
		}
		if err := deps.Repository.CreateDropPointWithinQuota(ctx, dp, maxActive, now); err != nil {
			if attempts < 2 && store.IsUniqueConstraint(err) {
				continue
			}
			return droppoint.CreateDropPointResponse{}, err
		}
		return droppoint.CreateDropPointResponse{
			DropPointID: id,
			DisplayName: displayName,
			DropToken:   dropToken,
			PickupToken: pickupToken,
			ExpiresAt:   dp.ExpiresAt,
			MaxBytes:    maxBytes,
		}, nil
	}
	return droppoint.CreateDropPointResponse{}, errors.New("could not allocate unique drop point identifiers")
}

func generateDropPointSecrets() (string, string, string, error) {
	id, err := token.GenerateDropPointID()
	if err != nil {
		return "", "", "", err
	}
	dropToken, err := token.GenerateDropToken()
	if err != nil {
		return "", "", "", err
	}
	pickupToken, err := token.GeneratePickupToken()
	if err != nil {
		return "", "", "", err
	}
	return id, dropToken, pickupToken, nil
}

func dropLink(baseURL string, dropToken string) string {
	base := strings.TrimRight(baseURL, "/")
	return fmt.Sprintf("%s/drop/%s", base, url.PathEscape(dropToken))
}
