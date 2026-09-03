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
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/cryptoenv"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestIntegrationReusableSessionSubmissionPickupAcknowledgeAndClose(t *testing.T) {
	repo, _, handler, apiPlain, logs := newIntegrationHarness(t)
	created := createViaAPI(t, handler, apiPlain)
	dropToken := dropTokenFromCreatedLink(t, created.DropLink)
	if strings.Contains(created.DropLink, "#") {
		t.Fatalf("drop link contains fragment: %s", created.DropLink)
	}

	recipientPrivate := sequenceBytesForIntegration(1, 32)
	recipientPublic, err := cryptoenv.PublicKeyFromPrivate(recipientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := cryptoenv.EncryptBundle(recipientPublic, []cryptoenv.PlainFile{{Name: "scan.txt", Type: "text/plain", Data: []byte("plaintext never reaches relay")}}, cryptoenv.EncryptOptions{
		SenderPrivateKey: sequenceBytesForIntegration(65, 32),
		MetadataNonce:    sequenceBytesForIntegration(129, 12),
		PayloadNonce:     sequenceBytesForIntegration(161, 12),
		CreatedAt:        time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}

	submit := httptest.NewRecorder()
	handler.ServeHTTP(submit, multipartDropRequest(t, submissionPath(dropToken, httpSubmissionOne), encrypted.EnvelopeJSON, encrypted.EncryptedPayload))
	if submit.Code != http.StatusOK {
		t.Fatalf("submit = %d %s", submit.Code, submit.Body.String())
	}
	status := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/status", created.PickupToken)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"status":"open"`) || !strings.Contains(status.Body.String(), `"pending_submissions":1`) {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	list := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/submissions", created.PickupToken)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), httpSubmissionOne) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}

	pickup := authorizedRequest(t, handler, http.MethodGet, pickupPath(created.DropPointID, httpSubmissionOne), created.PickupToken)
	if pickup.Code != http.StatusOK {
		t.Fatalf("pickup = %d %s", pickup.Code, pickup.Body.String())
	}
	gotEnvelopeJSON, gotPayload := readPickupMultipart(t, pickup)
	parsedEnvelope, err := cryptoenv.ParseEnvelopeJSON(gotEnvelopeJSON)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := cryptoenv.DecryptBundle(recipientPrivate, parsedEnvelope, gotPayload)
	if err != nil || len(files) != 1 || string(files[0].Data) != "plaintext never reaches relay" {
		t.Fatalf("decrypted files = %+v, err=%v", files, err)
	}
	picked, err := repo.FindSubmission(context.Background(), created.DropPointID, httpSubmissionOne)
	if err != nil || picked.FirstPickedUpAt == nil {
		t.Fatalf("picked = %+v, err=%v", picked, err)
	}

	ack := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+created.DropPointID+"/submissions/"+httpSubmissionOne, created.PickupToken)
	if ack.Code != http.StatusNoContent {
		t.Fatalf("ack = %d %s", ack.Code, ack.Body.String())
	}
	metadata := httptest.NewRecorder()
	handler.ServeHTTP(metadata, httptest.NewRequest(http.MethodGet, "/api/drops/"+dropToken, nil))
	if metadata.Code != http.StatusOK {
		t.Fatalf("session was not reusable after ack: %d %s", metadata.Code, metadata.Body.String())
	}
	submitDrop(t, handler, dropToken, httpSubmissionTwo, []byte("second ciphertext"))

	closed := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+created.DropPointID, created.PickupToken)
	if closed.Code != http.StatusNoContent {
		t.Fatalf("close = %d %s", closed.Code, closed.Body.String())
	}
	afterClose := httptest.NewRecorder()
	handler.ServeHTTP(afterClose, multipartDropRequest(t, submissionPath(dropToken, httpSubmissionThree), []byte(testEnvelopeJSON()), []byte("third")))
	if afterClose.Code != http.StatusGone {
		t.Fatalf("submit after close = %d %s", afterClose.Code, afterClose.Body.String())
	}

	for _, forbidden := range []string{apiPlain, dropToken, created.PickupToken, string(encrypted.EnvelopeJSON), cryptoenv.EncodeBase64URL(recipientPublic)} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("logs leaked sensitive value %q", forbidden)
		}
	}
	if !strings.Contains(logs.String(), "/api/drops/:drop_token/submissions/{submission_id}") {
		t.Fatalf("logs did not redact drop token path: %s", logs.String())
	}
}

func TestIntegrationSeparatesSenderAndReceiverCapabilities(t *testing.T) {
	_, _, handler, apiPlain, _ := newIntegrationHarness(t)
	created := createViaAPI(t, handler, apiPlain)
	dropToken := dropTokenFromCreatedLink(t, created.DropLink)

	wrongPickup := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/status", "pick_wrong")
	if wrongPickup.Code != http.StatusNotFound {
		t.Fatalf("wrong pickup = %d", wrongPickup.Code)
	}
	dropOnReceiver := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+created.DropPointID+"/status", dropToken)
	if dropOnReceiver.Code != http.StatusNotFound {
		t.Fatalf("drop token on receiver API = %d", dropOnReceiver.Code)
	}
	pickupOnDrop := httptest.NewRecorder()
	handler.ServeHTTP(pickupOnDrop, multipartDropRequest(t, submissionPath(created.PickupToken, httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
	if pickupOnDrop.Code != http.StatusNotFound {
		t.Fatalf("pickup token on sender API = %d", pickupOnDrop.Code)
	}
}

func TestIntegrationStorageFailureDoesNotEndSession(t *testing.T) {
	repo, _, handler, apiPlain, _ := newIntegrationHarnessWithBlob(t, failingBlobStore{})
	created := createViaAPI(t, handler, apiPlain)
	dropToken := dropTokenFromCreatedLink(t, created.DropLink)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath(dropToken, httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("storage failure = %d %s", recorder.Code, recorder.Body.String())
	}
	dp, err := repo.FindDropPointByID(context.Background(), created.DropPointID)
	if err != nil || dp.Status != droppoint.StatusOpen {
		t.Fatalf("session = %+v, err=%v", dp, err)
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
	return newIntegrationHarnessAt(t, filepath.Join(t.TempDir(), "data"), blob)
}

func newIntegrationHarnessAt(t *testing.T, dataDir string, blob BlobStore) (*store.Repository, *blobstore.Store, http.Handler, string, *bytes.Buffer) {
	t.Helper()
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewRepository(db.SQLDB())
	realBlobs, _ := blob.(*blobstore.Store)
	apiPlain := "api_integration_secret"
	if err := repo.AddAPIToken(context.Background(), store.AddAPITokenParams{ID: "integration", SecretHash: token.HashSecret(apiPlain), MaxActiveDropPoints: intPtr(10), CreatedAt: dropTestNow()}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.BaseURL = "https://drop.example.com"
	var logs bytes.Buffer
	handler := NewRouterWithDependencies(Dependencies{Config: cfg, Repository: repo, BlobStore: blob, Logger: log.New(&logs, "", 0), Now: dropTestNow})
	return repo, realBlobs, handler, apiPlain, &logs
}

func createViaAPI(t *testing.T, handler http.Handler, apiPlain string) createDropPointResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/drop-points", strings.NewReader(`{"ttl_seconds":600,"max_bytes":1024}`))
	request.Header.Set("Authorization", "Bearer "+apiPlain)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", recorder.Code, recorder.Body.String())
	}
	var created createDropPointResponse
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

func authorizedRequest(t *testing.T, handler http.Handler, method, path, bearer string) *httptest.ResponseRecorder {
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
		t.Fatal(err)
	}
	return strings.TrimPrefix(parsed.Path, "/drop/")
}

func sequenceBytesForIntegration(start byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = start + byte(i)
	}
	return out
}

type failingBlobStore struct{}

func (failingBlobStore) WriteSubmission(context.Context, string, string, []byte, io.Reader, int64) (droppoint.CommitSubmissionResult, error) {
	return droppoint.CommitSubmissionResult{}, errors.New("simulated disk full")
}

func (failingBlobStore) ReadEnvelope(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (failingBlobStore) OpenPayload(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, errors.New("not implemented")
}

func (failingBlobStore) DeleteSubmission(context.Context, string, string) error { return nil }
func (failingBlobStore) DeleteDropPoint(context.Context, string) error          { return nil }
