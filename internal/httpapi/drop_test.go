package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/blobstore"
	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/cryptoenv"
	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/store"
	"github.com/shunichironomura/drop-point/internal/token"
)

func TestSubmitDropStoresEncryptedPayload(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	now := dropTestNow()
	dp := testHTTPDropPoint(t, "dp_submit", "drop_submit", "pick_submit", now)
	dp.MaxBytes = 1024
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	payload := []byte{0, 1, 2, 3, 4, 5}
	envelope := []byte(testEnvelopeJSON())

	recorder := httptest.NewRecorder()
	request := multipartDropRequest(t, "/api/drops/drop_submit", envelope, payload)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response submitDropResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != droppoint.StatusReady {
		t.Fatalf("response status = %q, want ready", response.Status)
	}

	ready, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if ready.Status != droppoint.StatusReady || ready.EncryptedSize != int64(len(payload)) || ready.DroppedAt == nil {
		t.Fatalf("ready row mismatch: %+v", ready)
	}
	if got := readBlobPath(t, blobs, ready.PayloadPath); !bytes.Equal(got, payload) {
		t.Fatalf("payload bytes = %v, want %v", got, payload)
	}
	if got := readBlobPath(t, blobs, ready.EnvelopePath); !bytes.Equal(got, envelope) {
		t.Fatalf("envelope bytes = %q, want %q", got, envelope)
	}
}

func TestSubmitDropRejectsSecondDrop(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_second", "drop_second", "pick_second", dropTestNow())
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	for i, want := range []int{http.StatusOK, http.StatusConflict} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_second", []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != want {
			t.Fatalf("drop #%d status = %d body=%s, want %d", i+1, recorder.Code, recorder.Body.String(), want)
		}
	}
}

func TestSubmitDropRejectsOversizeAndResetsOpen(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_oversize", "drop_oversize", "pick_oversize", dropTestNow())
	dp.MaxBytes = 4
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_oversize", []byte(testEnvelopeJSON()), []byte("12345")))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	open, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if open.Status != droppoint.StatusOpen {
		t.Fatalf("status = %q, want open", open.Status)
	}
	if _, err := os.Stat(filepath.Join(blobs.DropDir(dp.ID), blobstore.PayloadFileName)); !os.IsNotExist(err) {
		t.Fatalf("payload final stat err = %v, want not exist", err)
	}
}

func TestSubmitDropRejectsMalformedMultipartWithoutConsumingSlot(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_bad_envelope", "drop_bad_envelope", "pick_bad_envelope", dropTestNow())
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_bad_envelope", []byte(`{"protocol_version":2}`), []byte("payload")))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	open, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_bad_envelope"), dropTestNow())
	if err != nil {
		t.Fatalf("FindOpenDropPointByDropTokenHash after malformed upload: %v", err)
	}
	if open.Status != droppoint.StatusOpen {
		t.Fatalf("status = %q, want open", open.Status)
	}
}

func TestSubmitDropAuthorizesOnlyDropToken(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_auth_drop", "drop_auth", "pick_auth", dropTestNow())
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	for name, path := range map[string]string{
		"unknown":      "/api/drops/drop_unknown",
		"pickup token": "/api/drops/pick_auth",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequest(t, path, []byte(testEnvelopeJSON()), []byte("payload")))
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestConcurrentDropAttemptsCommitAtMostOne(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_race", "drop_race", "pick_race", dropTestNow())
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_race", []byte(testEnvelopeJSON()), []byte("payload")))
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
		t.Fatalf("successful drops = %d, want 1", successes)
	}
}

func newDropTestHandler(t *testing.T) (*store.Repository, *blobstore.Store, http.Handler) {
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
	blobs := blobstore.New(dataDir)
	handler := NewRouterWithDependencies(Dependencies{
		Config:     config.Default(),
		Repository: repo,
		BlobStore:  blobs,
		Now:        dropTestNow,
	})
	return repo, blobs, handler
}

func multipartDropRequest(t *testing.T, path string, envelope []byte, payload []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	mustWritePart(t, writer, envelopePartName, jsonContentType, envelope)
	mustWritePart(t, writer, payloadPartName, octetContentType, payload)
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPut, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func mustWritePart(t *testing.T, writer *multipart.Writer, name string, contentType string, data []byte) {
	t.Helper()
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+name+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("CreatePart %s: %v", name, err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatalf("write part %s: %v", name, err)
	}
}

func testEnvelopeJSON() string {
	return `{"protocol_version":2,"key_agreement":"` + cryptoenv.KeyAgreement + `","sender_ephemeral_public_key":"` + cryptoenv.EncodeBase64URL(make([]byte, 32)) + `","metadata_nonce":"` + cryptoenv.EncodeBase64URL(make([]byte, 12)) + `","payload_nonce":"` + cryptoenv.EncodeBase64URL(make([]byte, 12)) + `","encrypted_metadata":"` + cryptoenv.EncodeBase64URL(make([]byte, 16)) + `"}`
}

func readBlobPath(t *testing.T, blobs *blobstore.Store, relative string) []byte {
	t.Helper()
	path, err := blobs.Path(relative)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}

func dropTestNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}
