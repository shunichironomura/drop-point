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

	"github.com/shunichironomura/droppoint/internal/dropname"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestGetDropMetadataReturnsSenderSafeSessionDetails(t *testing.T) {
	apiPlain := "api_valid"
	_, handler := newCreateTestHandler(t, apiTokenSeed{ID: "desktop-main", SecretHash: token.HashSecret(apiPlain), Enabled: true, MaxActiveDropPoints: intPtr(3)})
	createRecorder := httptest.NewRecorder()
	createRequest := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{"client_name":"test-client","ttl_seconds":120,"max_bytes":2048}`))
	createRequest.Header.Set("Authorization", "Bearer "+apiPlain)
	createRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(createRecorder, createRequest)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created createDropPointResponse
	if err := json.NewDecoder(createRecorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(created.DropLink)
	if err != nil {
		t.Fatal(err)
	}
	dropToken := strings.TrimPrefix(parsed.Path, "/drop/")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/drops/"+dropToken, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metadata status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var metadata dropMetadataResponse
	if err := json.NewDecoder(recorder.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.DisplayName != created.DisplayName || !dropname.Valid(metadata.DisplayName) || !metadata.ExpiresAt.Equal(created.ExpiresAt) {
		t.Fatalf("metadata = %+v created=%+v", metadata, created)
	}
	if metadata.MaxBytes != 2048 || metadata.MaxPendingSubmissions != 10 || metadata.MaxPendingBytes != 20_480 {
		t.Fatalf("metadata limits = %+v", metadata)
	}
	if strings.Contains(recorder.Body.String(), created.PickupToken) || strings.Contains(recorder.Body.String(), created.DropPointID) {
		t.Fatalf("metadata leaked receiver capability: %s", recorder.Body.String())
	}
}

func TestGetDropMetadataRemainsAvailableAfterSubmission(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_metadata_reuse", "drop_metadata_reuse", "pick_metadata_reuse", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	submit := httptest.NewRecorder()
	handler.ServeHTTP(submit, multipartDropRequest(t, submissionPath("drop_metadata_reuse", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
	if submit.Code != http.StatusOK {
		t.Fatalf("submit status = %d body=%s", submit.Code, submit.Body.String())
	}
	metadata := httptest.NewRecorder()
	handler.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/api/drops/drop_metadata_reuse", nil))
	if metadata.Code != http.StatusOK {
		t.Fatalf("metadata after submission = %d body=%s", metadata.Code, metadata.Body.String())
	}
}

func TestGetDropMetadataRejectsUnavailableSessionsWithoutDisplayName(t *testing.T) {
	repo, handler := newCreateTestHandler(t)
	now := dropTestNow()
	expired := metadataTestDropPoint(t, "dp_expired_metadata", "drop_expired_metadata", now.Add(-20*time.Minute))
	closed := metadataTestDropPoint(t, "dp_closed_metadata", "drop_closed_metadata", now)
	failed := metadataTestDropPoint(t, "dp_failed_metadata", "drop_failed_metadata", now)
	for _, dp := range []droppoint.DropPoint{expired, closed, failed} {
		insertHTTPDropPoint(t, repo, dp)
	}
	if err := repo.CloseDropPoint(context.Background(), closed.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.FailDropPoint(context.Background(), failed.ID, now); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		dropToken string
		want      int
	}{
		"unknown": {dropToken: "drop_unknown_metadata", want: http.StatusNotFound},
		"expired": {dropToken: "drop_expired_metadata", want: http.StatusGone},
		"closed":  {dropToken: "drop_closed_metadata", want: http.StatusConflict},
		"failed":  {dropToken: "drop_failed_metadata", want: http.StatusGone},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/drops/"+tc.dropToken, nil))
			if recorder.Code != tc.want || strings.Contains(recorder.Body.String(), "calm-otter") {
				t.Fatalf("response = %d %s, want %d without display name", recorder.Code, recorder.Body.String(), tc.want)
			}
		})
	}
}

func metadataTestDropPoint(t *testing.T, id, dropPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:                    id,
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         token.HashSecret(dropPlain),
		PickupTokenHash:       token.HashSecret("pick_" + id),
		TTL:                   10 * time.Minute,
		MaxBytes:              1024,
		MaxPendingSubmissions: 10,
		MaxPendingBytes:       10_240,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	return dp
}
