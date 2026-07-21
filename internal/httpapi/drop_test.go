package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

func TestSubmitDropStoresEncryptedPayload(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	now := dropTestNow()
	dp := testHTTPDropPoint(t, "dp_submit", "drop_submit", "pick_submit", now)
	dp.MaxBytes = 1024
	insertHTTPDropPoint(t, repo, dp)
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
	insertHTTPDropPoint(t, repo, dp)
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
	insertHTTPDropPoint(t, repo, dp)
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

func TestSubmitDropEnforcesNormativeMultipartFraming(t *testing.T) {
	tests := []struct {
		name  string
		parts []testMultipartPart
	}{
		{name: "payload before envelope", parts: []testMultipartPart{{payloadPartName, octetContentType, []byte("payload")}, {envelopePartName, jsonContentType, []byte(testEnvelopeJSON())}}},
		{name: "extra third part", parts: []testMultipartPart{{envelopePartName, jsonContentType, []byte(testEnvelopeJSON())}, {payloadPartName, octetContentType, []byte("payload")}, {"extra", octetContentType, []byte("extra")}}},
		{name: "envelope over one MiB", parts: []testMultipartPart{{envelopePartName, jsonContentType, bytes.Repeat([]byte(" "), maxEnvelopeBytes+1)}, {payloadPartName, octetContentType, []byte("payload")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, _, handler := newDropTestHandler(t)
			suffix := strings.NewReplacer(" ", "_", "-", "_").Replace(tt.name)
			dp := testHTTPDropPoint(t, "dp_framing_"+suffix, "drop_framing_"+suffix, "pick_framing_"+suffix, dropTestNow())
			insertHTTPDropPoint(t, repo, dp)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequestWithParts(t, "/api/drops/drop_framing_"+suffix, tt.parts))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want bad request", recorder.Code, recorder.Body.String())
			}
			assertDropStatus(t, repo, dp.ID, droppoint.StatusOpen)
		})
	}
}

func TestSubmitDropRejectsMalformedMultipartWithoutConsumingSlot(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_bad_envelope", "drop_bad_envelope", "pick_bad_envelope", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
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

func TestSubmitDropResetsOpenAfterCanceledPartialUpload(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_canceled_partial", "drop_canceled_partial", "pick_canceled_partial", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	body, contentType := multipartDropBody(t, []byte(testEnvelopeJSON()), []byte("partial-payload"))
	payloadOffset := bytes.Index(body, []byte("partial-payload"))
	if payloadOffset < 0 {
		t.Fatal("test payload bytes not found in multipart body")
	}
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPut, "/api/drops/drop_canceled_partial", &cancelAfterNReader{data: body, limit: payloadOffset + len("partial"), cancel: cancel}).WithContext(ctx)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	open, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_canceled_partial"), dropTestNow())
	if err != nil {
		t.Fatalf("FindOpenDropPointByDropTokenHash after canceled upload: %v", err)
	}
	if open.Status != droppoint.StatusOpen {
		t.Fatalf("status = %q, want open", open.Status)
	}
	if _, err := os.Stat(filepath.Join(blobs.DropDir(dp.ID), blobstore.PayloadFileName)); !os.IsNotExist(err) {
		t.Fatalf("payload final stat err = %v, want not exist", err)
	}
}

func TestSubmitDropCommitsAfterRequestContextCanceledPostUpload(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_canceled_commit", "drop_canceled_commit", "pick_canceled_commit", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	body, contentType := multipartDropBody(t, []byte(testEnvelopeJSON()), []byte("payload"))
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPut, "/api/drops/drop_canceled_commit", &cancelOnEOFReader{reader: bytes.NewReader(body), cancel: cancel}).WithContext(ctx)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	ready, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if ready.Status != droppoint.StatusReady {
		t.Fatalf("status = %q, want ready", ready.Status)
	}
}

func TestDropRequestSizeLimitRejectsOverflow(t *testing.T) {
	if _, err := dropRequestSizeLimit(math.MaxInt64); err == nil {
		t.Fatal("dropRequestSizeLimit accepted overflowing payload limit")
	}
	got, err := dropRequestSizeLimit(1024)
	if err != nil {
		t.Fatalf("dropRequestSizeLimit: %v", err)
	}
	if want := int64(1024 + maxEnvelopeBytes + multipartOverhead); got != want {
		t.Fatalf("dropRequestSizeLimit = %d, want %d", got, want)
	}
}

func TestWriteMultipartDropErrorMapsRequestLimitToPayloadTooLarge(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeMultipartDropError(recorder, &http.MaxBytesError{Limit: 1})
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSubmitDropAuthorizesOnlyDropToken(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_auth_drop", "drop_auth", "pick_auth", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
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

func TestSubmitDropMapsStorageFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "request limit", err: fmt.Errorf("read: %w", &http.MaxBytesError{Limit: 1}), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "disk full", err: fmt.Errorf("write: %w", syscall.ENOSPC), wantStatus: http.StatusInsufficientStorage},
		{name: "temporarily unavailable", err: fmt.Errorf("write: %w", syscall.EAGAIN), wantStatus: http.StatusServiceUnavailable},
		{name: "durability failure", err: errors.New("simulated fsync failure"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, blobs, _ := newDropTestHandler(t)
			dp := testHTTPDropPoint(t, "dp_storage_"+strings.ReplaceAll(tt.name, " ", "_"), "drop_storage", "pick_storage", dropTestNow())
			insertHTTPDropPoint(t, repo, dp)
			var logs bytes.Buffer
			handler := dropHandler(repo, &writeErrorBlobStore{BlobStore: blobs, err: tt.err}, &logs)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_storage", []byte(testEnvelopeJSON()), []byte("payload")))
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tt.wantStatus)
			}
			row, err := repo.FindDropPointByID(context.Background(), dp.ID)
			if err != nil {
				t.Fatalf("FindDropPointByID: %v", err)
			}
			if row.Status != droppoint.StatusOpen {
				t.Fatalf("status after storage failure = %q, want open", row.Status)
			}
			if !strings.Contains(logs.String(), "event=drop.failed") || strings.Contains(logs.String(), "drop_storage") {
				t.Fatalf("structured storage log missing or leaked capability: %s", logs.String())
			}
		})
	}
}

func TestSubmitDropFinalizationFailuresRemainRecoverable(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		repo, blobs, _ := newDropTestHandler(t)
		dp := testHTTPDropPoint(t, "dp_commit_failure", "drop_commit_failure", "pick_commit_failure", dropTestNow())
		insertHTTPDropPoint(t, repo, dp)
		handler := dropHandler(&repositoryOverride{Repository: repo, commitErr: errors.New("injected commit failure")}, blobs, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_commit_failure", []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		assertDropStatus(t, repo, dp.ID, droppoint.StatusOpen)
		if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("drop dir stat err = %v, want not exist", err)
		}
	})

	t.Run("ambiguous commit", func(t *testing.T) {
		repo, blobs, _ := newDropTestHandler(t)
		dp := testHTTPDropPoint(t, "dp_ambiguous_commit", "drop_ambiguous_commit", "pick_ambiguous_commit", dropTestNow())
		insertHTTPDropPoint(t, repo, dp)
		repository := &repositoryOverride{Repository: repo, errorAfterCommit: true}
		handler := dropHandler(repository, blobs, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_ambiguous_commit", []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		row, err := repo.FindDropPointByID(context.Background(), dp.ID)
		if err != nil {
			t.Fatalf("FindDropPointByID: %v", err)
		}
		if row.Status != droppoint.StatusFailed || row.FailedAt == nil {
			t.Fatalf("ambiguous commit row = %+v, want failed", row)
		}
		if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("drop dir stat err = %v, want not exist", err)
		}
	})

	t.Run("reset", func(t *testing.T) {
		repo, blobs, _ := newDropTestHandler(t)
		dp := testHTTPDropPoint(t, "dp_reset_failure", "drop_reset_failure", "pick_reset_failure", dropTestNow())
		insertHTTPDropPoint(t, repo, dp)
		repository := &repositoryOverride{Repository: repo, resetErr: errors.New("injected reset failure")}
		handler := dropHandler(repository, &writeErrorBlobStore{BlobStore: blobs, err: errors.New("injected write failure")}, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_reset_failure", []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		assertDropStatus(t, repo, dp.ID, droppoint.StatusReceiving)
		if _, err := (cleanup.Service{Repository: repo, BlobStore: blobs, Now: dropTestNow}).ReconcileStartup(context.Background()); err != nil {
			t.Fatalf("ReconcileStartup: %v", err)
		}
		assertDropStatus(t, repo, dp.ID, droppoint.StatusOpen)
	})

	t.Run("delete", func(t *testing.T) {
		repo, blobs, _ := newDropTestHandler(t)
		dp := testHTTPDropPoint(t, "dp_delete_failure", "drop_delete_failure", "pick_delete_failure", dropTestNow())
		insertHTTPDropPoint(t, repo, dp)
		failing := &deleteErrorBlobStore{BlobStore: blobs, err: errors.New("injected delete failure")}
		handler := dropHandler(repo, failing, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_delete_failure", []byte(`{"protocol_version":2}`), []byte("payload")))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
		}
		assertDropStatus(t, repo, dp.ID, droppoint.StatusReceiving)
		if _, err := (cleanup.Service{Repository: repo, BlobStore: blobs, Now: dropTestNow}).ReconcileStartup(context.Background()); err != nil {
			t.Fatalf("ReconcileStartup: %v", err)
		}
		assertDropStatus(t, repo, dp.ID, droppoint.StatusOpen)
	})
}

func TestSubmitDropReportsFailedPointGone(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_submit_failed", "drop_submit_failed", "pick_submit_failed", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	if err := repo.FailDropPoint(context.Background(), dp.ID, dropTestNow()); err != nil {
		t.Fatalf("FailDropPoint: %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/drop_submit_failed", []byte(testEnvelopeJSON()), []byte("payload")))
	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d body=%s, want gone", recorder.Code, recorder.Body.String())
	}
}

func TestConcurrentDropAttemptsCommitAtMostOne(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_race", "drop_race", "pick_race", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
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

type repositoryOverride struct {
	Repository
	commitErr        error
	resetErr         error
	errorAfterCommit bool
}

func (r *repositoryOverride) CommitReceivedDrop(ctx context.Context, id string, result droppoint.CommitDropResult, now time.Time) error {
	if r.commitErr != nil {
		return r.commitErr
	}
	if err := r.Repository.CommitReceivedDrop(ctx, id, result, now); err != nil {
		return err
	}
	if r.errorAfterCommit {
		return errors.New("injected ambiguous commit result")
	}
	return nil
}

func (r *repositoryOverride) ResetReceivingDrop(ctx context.Context, id string, now time.Time) error {
	if r.resetErr != nil {
		return r.resetErr
	}
	return r.Repository.ResetReceivingDrop(ctx, id, now)
}

type writeErrorBlobStore struct {
	BlobStore
	err error
}

func (s *writeErrorBlobStore) WriteDrop(context.Context, string, []byte, io.Reader, int64) (droppoint.CommitDropResult, error) {
	return droppoint.CommitDropResult{}, s.err
}

type deleteErrorBlobStore struct {
	BlobStore
	err    error
	failed bool
}

func (s *deleteErrorBlobStore) DeleteDropPoint(ctx context.Context, id string) error {
	if !s.failed {
		s.failed = true
		return s.err
	}
	return s.BlobStore.DeleteDropPoint(ctx, id)
}

func dropHandler(repository Repository, blobs BlobStore, logs *bytes.Buffer) http.Handler {
	logger := log.New(io.Discard, "", 0)
	if logs != nil {
		logger = log.New(logs, "", 0)
	}
	return NewRouterWithDependencies(Dependencies{Config: config.Default(), Repository: repository, BlobStore: blobs, Logger: logger, Now: dropTestNow})
}

func assertDropStatus(t *testing.T, repo *store.Repository, id string, want droppoint.Status) {
	t.Helper()
	row, err := repo.FindDropPointByID(context.Background(), id)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != want {
		t.Fatalf("status = %q, want %q", row.Status, want)
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

type testMultipartPart struct {
	name        string
	contentType string
	data        []byte
}

func multipartDropRequestWithParts(t *testing.T, path string, parts []testMultipartPart) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, part := range parts {
		mustWritePart(t, writer, part.name, part.contentType, part.data)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func multipartDropRequest(t *testing.T, path string, envelope []byte, payload []byte) *http.Request {
	t.Helper()
	body, contentType := multipartDropBody(t, envelope, payload)
	request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	return request
}

func multipartDropBody(t *testing.T, envelope []byte, payload []byte) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	mustWritePart(t, writer, envelopePartName, jsonContentType, envelope)
	mustWritePart(t, writer, payloadPartName, octetContentType, payload)
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body.Bytes(), writer.FormDataContentType()
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

type cancelAfterNReader struct {
	data     []byte
	limit    int
	offset   int
	cancel   context.CancelFunc
	canceled bool
}

func (r *cancelAfterNReader) Read(p []byte) (int, error) {
	if r.offset >= r.limit {
		if !r.canceled {
			r.cancel()
			r.canceled = true
		}
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, r.data[r.offset:r.limit])
	r.offset += n
	return n, nil
}

type cancelOnEOFReader struct {
	reader   *bytes.Reader
	cancel   context.CancelFunc
	canceled bool
}

func (r *cancelOnEOFReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF && !r.canceled {
		r.cancel()
		r.canceled = true
	}
	return n, err
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
