package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/cleanup"
	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/cryptoenv"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestIntegrationCreateDropStatusPickupClose(t *testing.T) {
	repo, _, handler, apiPlain, logs := newIntegrationHarness(t)
	recipientPrivate := sequenceBytesForIntegration(1, 32)
	recipientPublic, err := cryptoenv.PublicKeyFromPrivate(recipientPrivate)
	if err != nil {
		t.Fatalf("PublicKeyFromPrivate: %v", err)
	}

	created := createViaAPI(t, handler, apiPlain)
	parsedLink, err := url.Parse(created.DropLink)
	if err != nil {
		t.Fatalf("Parse drop link: %v", err)
	}
	dropToken := strings.TrimPrefix(parsedLink.Path, "/drop/")
	if strings.Contains(created.DropLink, "#") {
		t.Fatalf("drop link contains fragment: %s", created.DropLink)
	}

	encrypted, err := cryptoenv.EncryptBundle(recipientPublic, []cryptoenv.PlainFile{{Name: "scan.txt", Type: "text/plain", Data: []byte("plaintext never reaches relay")}}, cryptoenv.EncryptOptions{
		SenderPrivateKey: sequenceBytesForIntegration(65, 32),
		MetadataNonce:    sequenceBytesForIntegration(129, 12),
		PayloadNonce:     sequenceBytesForIntegration(161, 12),
		CreatedAt:        time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("EncryptBundle: %v", err)
	}

	dropRecorder := httptest.NewRecorder()
	handler.ServeHTTP(dropRecorder, multipartDropRequest(t, "/api/drops/"+dropToken, encrypted.EnvelopeJSON, encrypted.EncryptedPayload))
	if dropRecorder.Code != http.StatusOK {
		t.Fatalf("drop status = %d body=%s", dropRecorder.Code, dropRecorder.Body.String())
	}
	statusRecorder := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/status", created.PickupToken)
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `"status":"ready"`) {
		t.Fatalf("status response = %d %s", statusRecorder.Code, statusRecorder.Body.String())
	}

	pickupRecorder := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/pickup", created.PickupToken)
	if pickupRecorder.Code != http.StatusOK {
		t.Fatalf("pickup status = %d body=%s", pickupRecorder.Code, pickupRecorder.Body.String())
	}
	gotEnvelopeJSON, gotPayload := readPickupMultipart(t, pickupRecorder)
	parsedEnvelope, err := cryptoenv.ParseEnvelopeJSON(gotEnvelopeJSON)
	if err != nil {
		t.Fatalf("ParseEnvelopeJSON: %v", err)
	}
	files, _, err := cryptoenv.DecryptBundle(recipientPrivate, parsedEnvelope, gotPayload)
	if err != nil {
		t.Fatalf("DecryptBundle: %v", err)
	}
	if string(files[0].Data) != "plaintext never reaches relay" {
		t.Fatalf("decrypted data = %q", files[0].Data)
	}
	row, err := repo.FindDropPointByID(context.Background(), created.DropPointID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.FirstPickedUpAt == nil {
		t.Fatal("first pickup timestamp not recorded")
	}

	closeRecorder := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+created.DropPointID, created.PickupToken)
	if closeRecorder.Code != http.StatusNoContent {
		t.Fatalf("close status = %d body=%s", closeRecorder.Code, closeRecorder.Body.String())
	}
	afterClosePickup := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/pickup", created.PickupToken)
	if afterClosePickup.Code != http.StatusGone {
		t.Fatalf("pickup after close status = %d", afterClosePickup.Code)
	}

	for _, forbidden := range []string{apiPlain, dropToken, created.PickupToken, string(encrypted.EnvelopeJSON), cryptoenv.EncodeBase64URL(recipientPublic)} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("logs leaked sensitive value %q in %s", forbidden, logs.String())
		}
	}
	if !strings.Contains(logs.String(), "/api/drops/:drop_token") {
		t.Fatalf("logs did not redact drop path: %s", logs.String())
	}
}

func TestIntegrationFailureSecurityAndRetryCases(t *testing.T) {
	_, _, handler, apiPlain, _ := newIntegrationHarness(t)
	created := createViaAPI(t, handler, apiPlain)
	dropToken := dropTokenFromCreatedLink(t, created.DropLink)

	wrongPickup := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/status", "pick_wrong")
	if wrongPickup.Code != http.StatusNotFound {
		t.Fatalf("wrong pickup status = %d", wrongPickup.Code)
	}
	dropTokenOnReceiver := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/status", dropToken)
	if dropTokenOnReceiver.Code != http.StatusNotFound {
		t.Fatalf("drop token receiver status = %d", dropTokenOnReceiver.Code)
	}
	pickupTokenOnDrop := httptest.NewRecorder()
	handler.ServeHTTP(pickupTokenOnDrop, multipartDropRequest(t, "/api/drops/"+created.PickupToken, []byte(testEnvelopeJSON()), []byte("payload")))
	if pickupTokenOnDrop.Code != http.StatusNotFound {
		t.Fatalf("pickup token on drop status = %d", pickupTokenOnDrop.Code)
	}

	badDrop := httptest.NewRecorder()
	handler.ServeHTTP(badDrop, multipartDropRequest(t, "/api/drops/"+dropToken, []byte(`{"protocol_version":2}`), []byte("payload")))
	if badDrop.Code != http.StatusBadRequest {
		t.Fatalf("bad drop status = %d", badDrop.Code)
	}
	goodDrop := httptest.NewRecorder()
	handler.ServeHTTP(goodDrop, multipartDropRequest(t, "/api/drops/"+dropToken, []byte(testEnvelopeJSON()), []byte("payload")))
	if goodDrop.Code != http.StatusOK {
		t.Fatalf("retry drop status = %d body=%s", goodDrop.Code, goodDrop.Body.String())
	}
	secondPickup := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/pickup", created.PickupToken)
	if secondPickup.Code != http.StatusOK {
		t.Fatalf("pickup after retry status = %d", secondPickup.Code)
	}
	repeatedPickup := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/pickup", created.PickupToken)
	if repeatedPickup.Code != http.StatusOK {
		t.Fatalf("repeated pickup status = %d", repeatedPickup.Code)
	}

	closed := createViaAPI(t, handler, apiPlain)
	closeRecorder := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+closed.DropPointID, closed.PickupToken)
	if closeRecorder.Code != http.StatusNoContent {
		t.Fatalf("close before drop status = %d", closeRecorder.Code)
	}
	closedDrop := httptest.NewRecorder()
	handler.ServeHTTP(closedDrop, multipartDropRequest(t, "/api/drops/"+dropTokenFromCreatedLink(t, closed.DropLink), []byte(testEnvelopeJSON()), []byte("payload")))
	if closedDrop.Code != http.StatusGone {
		t.Fatalf("closed drop status = %d", closedDrop.Code)
	}
}

func TestIntegrationConcurrentOversizeCleanupAndDiskFailure(t *testing.T) {
	repo, blobs, handler, apiPlain, _ := newIntegrationHarness(t)
	created := createViaAPI(t, handler, apiPlain)
	dropToken := dropTokenFromCreatedLink(t, created.DropLink)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/"+dropToken, []byte(testEnvelopeJSON()), []byte("payload")))
			statuses <- recorder.Code
		}()
	}
	wg.Wait()
	close(statuses)
	successes := 0
	for status := range statuses {
		if status == http.StatusOK {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent successes = %d, want 1", successes)
	}

	oversized := createViaAPI(t, handler, apiPlain)
	overDropToken := dropTokenFromCreatedLink(t, oversized.DropLink)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/"+overDropToken, []byte(testEnvelopeJSON()), bytes.Repeat([]byte("x"), int(oversized.MaxBytes)+1)))
	if recorder.Code != http.StatusRequestEntityTooLarge && recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversize status = %d", recorder.Code)
	}

	expiredReady := createDropPointDirect(t, repo, "dp_expired_ready", "drop_expired_ready", "pick_expired_ready", dropTestNow().Add(-20*time.Minute))
	result, err := blobs.WriteDrop(context.Background(), expiredReady.ID, []byte(testEnvelopeJSON()), strings.NewReader("payload"), 1024)
	if err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	if err := repo.BeginReceivingDrop(context.Background(), expiredReady.ID, dropTestNow().Add(-19*time.Minute)); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), expiredReady.ID, result, dropTestNow().Add(-18*time.Minute)); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}
	createDropPointDirect(t, repo, "dp_expired_open", "drop_expired_open", "pick_expired_open", dropTestNow().Add(-20*time.Minute))
	cleanupResult, err := (cleanup.Service{Repository: repo, BlobStore: blobs, Now: dropTestNow}).Expire(context.Background())
	if err != nil {
		t.Fatalf("cleanup Expire: %v", err)
	}
	if cleanupResult.ExpiredDropPoints < 2 || cleanupResult.DeletedPayloads < 1 {
		t.Fatalf("cleanup result = %+v", cleanupResult)
	}

	failRepo, _, failHandler, failAPIPlain, _ := newIntegrationHarnessWithBlob(t, failingBlobStore{})
	failed := createViaAPI(t, failHandler, failAPIPlain)
	failedRecorder := httptest.NewRecorder()
	failHandler.ServeHTTP(failedRecorder, multipartDropRequest(t, "/api/drops/"+dropTokenFromCreatedLink(t, failed.DropLink), []byte(testEnvelopeJSON()), []byte("payload")))
	if failedRecorder.Code != http.StatusBadRequest {
		t.Fatalf("disk failure status = %d body=%s", failedRecorder.Code, failedRecorder.Body.String())
	}
	row, err := failRepo.FindDropPointByID(context.Background(), failed.DropPointID)
	if err != nil {
		t.Fatalf("FindDropPointByID after disk failure: %v", err)
	}
	if row.Status != droppoint.StatusOpen {
		t.Fatalf("status after disk failure = %q, want open", row.Status)
	}
}

func TestCORSAllowsConfiguredSameOriginOnly(t *testing.T) {
	_, _, handler, _, _ := newIntegrationHarness(t)
	request := httptest.NewRequest(http.MethodOptions, "/api/drop-points", nil)
	request.Header.Set("Origin", "https://drop.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || recorder.Header().Get("Access-Control-Allow-Origin") != "https://drop.example.com" {
		t.Fatalf("same-origin preflight = %d origin=%q", recorder.Code, recorder.Header().Get("Access-Control-Allow-Origin"))
	}

	request = httptest.NewRequest(http.MethodOptions, "/api/drop-points", nil)
	request.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("cross-origin preflight = %d origin=%q", recorder.Code, recorder.Header().Get("Access-Control-Allow-Origin"))
	}
}

func newIntegrationHarness(t *testing.T) (*store.Repository, *blobstore.Store, http.Handler, string, *bytes.Buffer) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	blobs := blobstore.New(dataDir)
	return newIntegrationHarnessAt(t, dataDir, blobs)
}

func newIntegrationHarnessWithBlob(t *testing.T, blob BlobStore) (*store.Repository, *blobstore.Store, http.Handler, string, *bytes.Buffer) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	return newIntegrationHarnessAt(t, dataDir, blob)
}

func newIntegrationHarnessAt(t *testing.T, dataDir string, blob BlobStore) (*store.Repository, *blobstore.Store, http.Handler, string, *bytes.Buffer) {
	t.Helper()
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	db, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewRepository(db.SQLDB())
	realBlobs, _ := blob.(*blobstore.Store)
	apiPlain := "api_integration_secret"
	if err := repo.AddAPIToken(context.Background(), store.AddAPITokenParams{ID: "integration", SecretHash: token.HashSecret(apiPlain), MaxActiveDropPoints: intPtr(10), CreatedAt: dropTestNow()}); err != nil {
		t.Fatalf("AddAPIToken: %v", err)
	}
	cfg := config.Default()
	cfg.BaseURL = "https://drop.example.com"
	var logs bytes.Buffer
	handler := NewRouterWithDependencies(Dependencies{
		Config:     cfg,
		Repository: repo,
		BlobStore:  blob,
		Logger:     log.New(&logs, "", 0),
		Now:        dropTestNow,
	})
	return repo, realBlobs, handler, apiPlain, &logs
}

func createViaAPI(t *testing.T, handler http.Handler, apiPlain string) createDropPointResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{"ttl_seconds":600,"max_bytes":1024,"single_use":true}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var created createDropPointResponse
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	return created
}

func authorizedRequest(t *testing.T, handler http.Handler, method string, path string, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	handler.ServeHTTP(recorder, request)
	return recorder
}

func dropTokenFromCreatedLink(t *testing.T, link string) string {
	t.Helper()
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("Parse drop link: %v", err)
	}
	return strings.TrimPrefix(parsed.Path, "/drop/")
}

func createDropPointDirect(t *testing.T, repo *store.Repository, id string, dropPlain string, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp := testHTTPDropPoint(t, id, dropPlain, pickupPlain, now)
	insertHTTPDropPoint(t, repo, dp)
	return dp
}

func sequenceBytesForIntegration(start byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = start + byte(i)
	}
	return out
}

type failingBlobStore struct{}

func (failingBlobStore) WriteDrop(context.Context, string, []byte, io.Reader, int64) (droppoint.CommitDropResult, error) {
	return droppoint.CommitDropResult{}, errors.New("simulated disk full")
}

func (failingBlobStore) ReadEnvelope(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (failingBlobStore) OpenPayload(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (failingBlobStore) DeleteDropPoint(context.Context, string) error {
	return nil
}
