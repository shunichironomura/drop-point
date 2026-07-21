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

	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/dropname"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestCreateDropPointWithValidAPIToken(t *testing.T) {
	ctx := context.Background()
	apiPlain := "api_valid"
	repo, handler := newCreateTestHandler(t, apiTokenSeed{ID: "desktop-main", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(3)})

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
	if !token.EqualHash(token.HashSecret(strings.TrimPrefix(parsed.Path, "/drop/")), dp.DropTokenHash) {
		t.Fatal("stored drop token hash does not verify drop link token")
	}
	if !token.EqualHash(token.HashSecret(response.PickupToken), dp.PickupTokenHash) {
		t.Fatal("stored pickup token hash does not verify pickup token")
	}
}

func TestCreateDropPointRejectsInvalidAPITokens(t *testing.T) {
	validPlain := "api_valid"
	disabledPlain := "api_disabled"
	_, handler := newCreateTestHandler(t,
		apiTokenSeed{ID: "valid", SecretHash: token.HashSecret(validPlain), Enabled: true},
		apiTokenSeed{ID: "disabled", SecretHash: token.HashSecret(disabledPlain), Enabled: false},
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
	_, handler := newCreateTestHandler(t, apiTokenSeed{ID: "limited", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(1)})

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
			request.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("first create status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("quota status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateDropPointRequiresPresenceAwareJSONObject(t *testing.T) {
	apiPlain := "api_presence"
	repo, handler := newCreateTestHandler(t, apiTokenSeed{ID: "presence", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(100)})

	for _, tt := range []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{name: "omitted optionals use defaults", body: `{}`, contentType: "application/json", wantStatus: http.StatusCreated},
		{name: "JSON charset parameter accepted", body: `{"single_use":true}`, contentType: "application/json; charset=utf-8", wantStatus: http.StatusCreated},
		{name: "null root", body: `null`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "array root", body: `[]`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "zero TTL", body: `{"ttl_seconds":0}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "null TTL", body: `{"ttl_seconds":null}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "zero max bytes", body: `{"max_bytes":0}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "null max bytes", body: `{"max_bytes":null}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "false single use", body: `{"single_use":false}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "null single use", body: `{"single_use":null}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "null client name", body: `{"client_name":null}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "non-string client name", body: `{"client_name":42}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "blank client name", body: `{"client_name":"  "}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "control in client name", body: `{"client_name":"bad\u000aname"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "format in client name", body: `{"client_name":"bad\u200dname"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "oversize client name", body: `{"client_name":"` + strings.Repeat("a", maxClientNameUTF8Bytes+1) + `"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "missing content type", body: `{}`, contentType: "", wantStatus: http.StatusUnsupportedMediaType},
		{name: "wrong content type", body: `{}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
	} {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(tt.body))
			request.Header.Set("Authorization", "Bearer "+apiPlain)
			if tt.contentType != "" {
				request.Header.Set("Content-Type", tt.contentType)
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tt.wantStatus)
			}
		})
	}

	rows, err := repo.DropPointIDs(context.Background())
	if err != nil {
		t.Fatalf("DropPointIDs: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("created rows = %d, want only two valid requests", len(rows))
	}
}

func TestCreateDropPointMapsOversizeRequestTo413(t *testing.T) {
	apiPlain := "api_oversize_create"
	_, handler := newCreateTestHandler(t, apiTokenSeed{ID: "oversize", SecretHash: token.HashSecret(apiPlain), Enabled: true})
	body := `{"client_name":"` + strings.Repeat("a", maxCreateRequestBytes) + `"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s, want 413", recorder.Code, recorder.Body.String())
	}
}

type apiTokenSeed struct {
	ID                  string
	SecretHash          string
	Enabled             bool
	MaxActiveDropPoints *int
}

func newCreateTestHandler(t *testing.T, apiTokens ...apiTokenSeed) (*store.Repository, http.Handler) {
	t.Helper()
	return newCreateTestHandlerWithBlob(t, nil, apiTokens...)
}

func newCreateTestHandlerWithBlob(t *testing.T, blob BlobStore, apiTokens ...apiTokenSeed) (*store.Repository, http.Handler) {
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
	insertAPITokenSeeds(t, repo, apiTokens, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))
	cfg := config.Default()
	cfg.BaseURL = "https://drop.example.com"
	handler := NewRouterWithDependencies(Dependencies{
		Config:     cfg,
		Repository: repo,
		BlobStore:  blob,
		Logger:     log.New(&bytes.Buffer{}, "", 0),
		Now:        func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) },
	})
	return repo, handler
}

func insertAPITokenSeeds(t *testing.T, repo *store.Repository, apiTokens []apiTokenSeed, now time.Time) {
	t.Helper()
	for _, seed := range apiTokens {
		if err := repo.AddAPIToken(context.Background(), store.AddAPITokenParams{ID: seed.ID, SecretHash: seed.SecretHash, MaxActiveDropPoints: seed.MaxActiveDropPoints, CreatedAt: now}); err != nil {
			t.Fatalf("AddAPIToken %s: %v", seed.ID, err)
		}
		if !seed.Enabled {
			if err := repo.DisableAPIToken(context.Background(), seed.ID, now.Add(time.Second)); err != nil {
				t.Fatalf("DisableAPIToken %s: %v", seed.ID, err)
			}
		}
	}
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
