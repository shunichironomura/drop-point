package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/shunichironomura/droppoint/internal/dropname"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

const (
	maxCreateRequestBytes  = 1 << 20
	maxClientNameUTF8Bytes = 128
)

type presentValue[T any] struct {
	Value   T
	Present bool
	Null    bool
}

func (v *presentValue[T]) UnmarshalJSON(data []byte) error {
	v.Present = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		v.Null = true
		return nil
	}
	return json.Unmarshal(data, &v.Value)
}

type createDropPointRequest struct {
	ClientName presentValue[string] `json:"client_name"`
	TTLSeconds presentValue[int]    `json:"ttl_seconds"`
	MaxBytes   presentValue[int64]  `json:"max_bytes"`
	SingleUse  presentValue[bool]   `json:"single_use"`
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
		authenticated, ok, err := authenticateAPIToken(r.Context(), deps.Repository, deps.Config.DefaultMaxActiveDropPoints, r.Header.Get("Authorization"))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "api_token_auth_failed", "could not authenticate API token")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "api_token_invalid", "valid bearer API token required")
			return
		}

		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != jsonContentType {
			writeError(w, http.StatusUnsupportedMediaType, "content_type_invalid", "Content-Type must be application/json")
			return
		}
		req, ok := decodeCreateRequest(w, r)
		if !ok {
			return
		}

		ttlSeconds, maxBytes, ok := validateCreateRequest(w, deps, req)
		if !ok {
			return
		}

		now := deps.Now().UTC()
		created, err := createDropPoint(r.Context(), deps, authenticated.ID, authenticated.MaxActiveDropPoints, req.ClientName.Value, time.Duration(ttlSeconds)*time.Second, maxBytes, now)
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

func decodeCreateRequest(w http.ResponseWriter, r *http.Request) (createDropPointRequest, bool) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCreateRequestBytes))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "create request exceeds the size limit")
			return createDropPointRequest{}, false
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a non-null JSON object")
		return createDropPointRequest{}, false
	}
	var trailing any
	switch err := decoder.Decode(&trailing); {
	case errors.Is(err, io.EOF):
	case err == nil:
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value")
		return createDropPointRequest{}, false
	default:
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be valid JSON")
		return createDropPointRequest{}, false
	}
	trimmed := bytes.TrimSpace(raw)
	if !utf8.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must be a non-null JSON object")
		return createDropPointRequest{}, false
	}
	requestDecoder := json.NewDecoder(bytes.NewReader(trimmed))
	requestDecoder.DisallowUnknownFields()
	var req createDropPointRequest
	if err := requestDecoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "request body must use the documented field types")
		return createDropPointRequest{}, false
	}
	return req, true
}

func validateCreateRequest(w http.ResponseWriter, deps Dependencies, req createDropPointRequest) (int, int64, bool) {
	if req.ClientName.Null || (req.ClientName.Present && !validClientName(req.ClientName.Value)) {
		writeError(w, http.StatusBadRequest, "client_name_invalid", "client_name must be a non-blank string of at most 128 UTF-8 bytes without control characters")
		return 0, 0, false
	}
	if req.SingleUse.Null || (req.SingleUse.Present && !req.SingleUse.Value) {
		writeError(w, http.StatusBadRequest, "single_use_required", "single_use must be true when present")
		return 0, 0, false
	}
	if req.TTLSeconds.Null {
		writeError(w, http.StatusBadRequest, "ttl_seconds_invalid", "ttl_seconds must be a positive integer when present")
		return 0, 0, false
	}
	ttlSeconds := req.TTLSeconds.Value
	if !req.TTLSeconds.Present {
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
	if req.MaxBytes.Null {
		writeError(w, http.StatusBadRequest, "max_bytes_invalid", "max_bytes must be a positive integer when present")
		return 0, 0, false
	}
	maxBytes := req.MaxBytes.Value
	if !req.MaxBytes.Present {
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

func validClientName(value string) bool {
	if value == "" || !utf8.ValidString(value) || len(value) > maxClientNameUTF8Bytes || strings.TrimSpace(value) == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return false
		}
	}
	return true
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
