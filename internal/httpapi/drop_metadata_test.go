package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/dropname"
	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/token"
)

func TestGetDropMetadataReturnsServerBoundDisplayName(t *testing.T) {
	apiPlain := "api_valid"
	_, handler := newCreateTestHandler(t, config.APIToken{ID: "desktop-main", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(3)})

	createRecorder := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{"client_name":"test-client","ttl_seconds":120,"max_bytes":2048,"single_use":true}`))
	createRequest.Header.Set("Authorization", "Bearer "+apiPlain)
	handler.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRecorder.Code, createRecorder.Body.String())
	}

	var created createDropPointResponse
	if err := json.NewDecoder(createRecorder.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	parsed, err := url.Parse(created.DropLink)
	if err != nil {
		t.Fatalf("parse drop_link: %v", err)
	}
	dropToken := strings.TrimPrefix(parsed.Path, "/drop/")

	metadataRecorder := httptest.NewRecorder()
	metadataRequest := httptest.NewRequest(http.MethodGet, "/api/drops/"+dropToken, nil)
	handler.ServeHTTP(metadataRecorder, metadataRequest)
	if metadataRecorder.Code != http.StatusOK {
		t.Fatalf("metadata status = %d body=%s", metadataRecorder.Code, metadataRecorder.Body.String())
	}

	var metadata dropMetadataResponse
	if err := json.NewDecoder(metadataRecorder.Body).Decode(&metadata); err != nil {
		t.Fatalf("decode metadata response: %v", err)
	}
	if metadata.DisplayName != created.DisplayName || !dropname.Valid(metadata.DisplayName) {
		t.Fatalf("display_name = %q, want created name %q", metadata.DisplayName, created.DisplayName)
	}
	if !metadata.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("expires_at = %s, want %s", metadata.ExpiresAt, created.ExpiresAt)
	}
	if metadata.MaxBytes != created.MaxBytes {
		t.Fatalf("max_bytes = %d, want %d", metadata.MaxBytes, created.MaxBytes)
	}
}

func TestGetDropMetadataRejectsUnknownExpiredAndUsedDrops(t *testing.T) {
	repo, handler := newCreateTestHandler(t, config.APIToken{ID: "desktop-main", SecretHash: token.HashSecret("api_valid"), Enabled: true})
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	expired := metadataTestDropPoint(t, "dp_expired_metadata", "drop_expired_metadata", now.Add(-20*time.Minute))
	ready := metadataTestDropPoint(t, "dp_ready_metadata", "drop_ready_metadata", now)
	ready.Status = droppoint.StatusReady
	droppedAt := now.Add(time.Second)
	ready.DroppedAt = &droppedAt
	ready.EnvelopePath = "drop-points/dp_ready_metadata/envelope.json"
	ready.PayloadPath = "drop-points/dp_ready_metadata/payload.bin"
	ready.EncryptedSize = 42
	for _, dp := range []droppoint.DropPoint{expired, ready} {
		if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
			t.Fatalf("CreateDropPoint %s: %v", dp.ID, err)
		}
	}

	tests := map[string]struct {
		token string
		want  int
	}{
		"unknown": {token: "drop_unknown_metadata", want: http.StatusNotFound},
		"expired": {token: "drop_expired_metadata", want: http.StatusGone},
		"used":    {token: "drop_ready_metadata", want: http.StatusConflict},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/drops/"+tc.token, nil)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.want)
			}
		})
	}
}

func metadataTestDropPoint(t *testing.T, id string, dropPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:              id,
		APITokenID:      "desktop-main",
		DisplayName:     "calm-otter",
		DropTokenHash:   token.HashSecret(dropPlain),
		PickupTokenHash: token.HashSecret("pick_" + id),
		TTL:             10 * time.Minute,
		MaxBytes:        1024,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	return dp
}
