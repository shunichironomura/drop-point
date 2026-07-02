package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/dropname"
	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/store"
	"github.com/shunichironomura/drop-point/internal/token"
)

func TestCreateDropPointWithValidAPIToken(t *testing.T) {
	ctx := context.Background()
	apiPlain := "api_valid"
	repo, handler := newCreateTestHandler(t, config.APIToken{ID: "desktop-main", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(3)})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{"client_name":"test-client","ttl_seconds":120,"max_bytes":2048,"single_use":true}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	var response createDropPointResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(response.DropPointID, token.DropPointIDPrefix) {
		t.Fatalf("drop_point_id = %q", response.DropPointID)
	}
	if !strings.HasPrefix(response.PickupToken, token.PickupTokenPrefix) {
		t.Fatalf("pickup_token = %q", response.PickupToken)
	}
	if !dropname.Valid(response.DisplayName) {
		t.Fatalf("display_name = %q, want adjective-noun", response.DisplayName)
	}
	if strings.Contains(response.DropLink, "#") {
		t.Fatalf("drop_link contains fragment: %q", response.DropLink)
	}
	parsed, err := url.Parse(response.DropLink)
	if err != nil {
		t.Fatalf("parse drop_link: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "drop.example.com" || !strings.HasPrefix(parsed.Path, "/drop/drop_") {
		t.Fatalf("drop_link = %q", response.DropLink)
	}

	dp, err := repo.FindDropPointByID(ctx, response.DropPointID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if dp.Status != droppoint.StatusOpen || dp.APITokenID != "desktop-main" || dp.ClientName != "test-client" || dp.DisplayName != response.DisplayName {
		t.Fatalf("stored drop point mismatch: %+v", dp)
	}
	if dp.DropTokenHash == strings.TrimPrefix(parsed.Path, "/drop/") || dp.PickupTokenHash == response.PickupToken {
		t.Fatalf("raw tokens were stored in row: %+v response=%+v", dp, response)
	}
	if !token.VerifySecretHash(strings.TrimPrefix(parsed.Path, "/drop/"), dp.DropTokenHash) {
		t.Fatal("stored drop token hash does not verify drop link token")
	}
	if !token.VerifySecretHash(response.PickupToken, dp.PickupTokenHash) {
		t.Fatal("stored pickup token hash does not verify pickup token")
	}
}

func TestCreateDropPointRejectsInvalidAPITokens(t *testing.T) {
	validPlain := "api_valid"
	disabledPlain := "api_disabled"
	_, handler := newCreateTestHandler(t,
		config.APIToken{ID: "valid", SecretHash: token.HashSecret(validPlain), Enabled: true},
		config.APIToken{ID: "disabled", SecretHash: token.HashSecret(disabledPlain), Enabled: false},
	)

	tests := map[string]string{
		"missing":   "",
		"malformed": "Basic abc",
		"disabled":  "Bearer " + disabledPlain,
		"wrong":     "Bearer api_wrong",
	}
	for name, authorization := range tests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{}`))
			if authorization != "" {
				request.Header.Set("Authorization", authorization)
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCreateDropPointValidatesLimitsAndQuota(t *testing.T) {
	apiPlain := "api_limited"
	_, handler := newCreateTestHandler(t, config.APIToken{ID: "limited", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(1)})

	badRequests := map[string]string{
		"ttl too large":       `{"ttl_seconds":901}`,
		"max bytes too large": `{"max_bytes":52428801}`,
		"single use false":    `{"single_use":false}`,
	}
	for name, body := range badRequests {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+apiPlain)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("first create status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newCreateTestHandler(t *testing.T, apiTokens ...config.APIToken) (*store.Repository, http.Handler) {
	t.Helper()
	return newCreateTestHandlerWithBlob(t, nil, apiTokens...)
}

func newCreateTestHandlerWithBlob(t *testing.T, blob BlobStore, apiTokens ...config.APIToken) (*store.Repository, http.Handler) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	db, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewRepository(db.SQLDB())
	cfg := config.Default()
	cfg.BaseURL = "https://drop.example.com"
	cfg.APITokens = apiTokens
	handler := NewRouterWithDependencies(Dependencies{
		Config:     cfg,
		Repository: repo,
		BlobStore:  blob,
		Logger:     log.New(&bytes.Buffer{}, "", 0),
		Now:        func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) },
	})
	return repo, handler
}

func insertHTTPDropPoint(t *testing.T, repo *store.Repository, dp droppoint.DropPoint) {
	t.Helper()
	if err := repo.CreateDropPointWithinQuota(context.Background(), dp, 1_000_000, dp.CreatedAt); err != nil {
		t.Fatalf("CreateDropPointWithinQuota %s: %v", dp.ID, err)
	}
}

func intPtr(value int) *int {
	return &value
}
