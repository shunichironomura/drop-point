package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/cryptoenv"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
)

const (
	httpSubmissionOne   = "sub_AAAAAAAAAAAAAAAAAAAAAA"
	httpSubmissionTwo   = "sub_AQEBAQEBAQEBAQEBAQEBAQ"
	httpSubmissionThree = "sub_AgICAgICAgICAgICAgICAg"
)

func TestSubmitDropQueuesMultipleImmutableSubmissions(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_submit", "drop_submit", "pick_submit", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)

	for _, tc := range []struct {
		id      string
		payload []byte
	}{
		{id: httpSubmissionOne, payload: []byte("first")},
		{id: httpSubmissionTwo, payload: []byte("second")},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_submit", tc.id), []byte(testEnvelopeJSON()), tc.payload))
		if recorder.Code != http.StatusOK {
			t.Fatalf("submit %s = %d %s", tc.id, recorder.Code, recorder.Body.String())
		}
		var response submitDropResponse
		if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.SubmissionID != tc.id || response.Status != droppoint.SubmissionStatusReady {
			t.Fatalf("response = %+v", response)
		}
		stored, err := repo.FindSubmission(context.Background(), dp.ID, tc.id)
		if err != nil || stored.Status != droppoint.SubmissionStatusReady || stored.DroppedAt == nil {
			t.Fatalf("stored = %+v, err=%v", stored, err)
		}
		if got := readBlobPath(t, blobs, stored.PayloadPath); !bytes.Equal(got, tc.payload) {
			t.Fatalf("payload = %q, want %q", got, tc.payload)
		}
	}
	parent, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), dp.DropTokenHash, dropTestNow())
	if err != nil || parent.Status != droppoint.StatusOpen {
		t.Fatalf("parent session = %+v, err=%v", parent, err)
	}
}

func TestSubmitDropRetryDoesNotReplaceCommittedCiphertext(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_retry", "drop_retry", "pick_retry", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, "drop_retry", httpSubmissionOne, []byte("original"))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_retry", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("replacement")))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"ready"`) {
		t.Fatalf("retry = %d %s", recorder.Code, recorder.Body.String())
	}
	stored, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil {
		t.Fatal(err)
	}
	if got := readBlobPath(t, blobs, stored.PayloadPath); string(got) != "original" {
		t.Fatalf("payload was replaced: %q", got)
	}
	if err := repo.AcknowledgeSubmission(context.Background(), dp.ID, httpSubmissionOne, dropTestNow()); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_retry", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("replacement")))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"acknowledged"`) {
		t.Fatalf("acknowledged retry = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestSubmitDropEnforcesCountQueueAndFreesItAfterAcknowledgement(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_count", "drop_count", "pick_count", dropTestNow())
	dp.MaxPendingSubmissions = 1
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, "drop_count", httpSubmissionOne, []byte("first"))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_count", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("replacement")))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"ready"`) {
		t.Fatalf("immutable retry at full queue = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_count", httpSubmissionTwo), []byte(testEnvelopeJSON()), []byte("second")))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("full queue = %d %s", recorder.Code, recorder.Body.String())
	}
	ack := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+dp.ID+"/submissions/"+httpSubmissionOne, "pick_count")
	if ack.Code != http.StatusNoContent {
		t.Fatalf("ack = %d %s", ack.Code, ack.Body.String())
	}
	submitDrop(t, handler, "drop_count", httpSubmissionTwo, []byte("second"))
}

func TestSubmitDropEnforcesPendingByteQueue(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_bytes", "drop_bytes", "pick_bytes", dropTestNow())
	dp.MaxBytes = 6
	dp.MaxPendingBytes = 6
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, "drop_bytes", httpSubmissionOne, []byte("1234"))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_bytes", httpSubmissionTwo), []byte(testEnvelopeJSON()), []byte("567")))
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("byte-full queue = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionTwo); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		t.Fatalf("rejected submission row error = %v", err)
	}
}

func TestSubmitDropMalformedAttemptCanRetrySameID(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_retry_bad", "drop_retry_bad", "pick_retry_bad", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_retry_bad", httpSubmissionOne), []byte(`{"protocol_version":2}`), []byte("payload")))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		t.Fatalf("failed attempt row error = %v", err)
	}
	submitDrop(t, handler, "drop_retry_bad", httpSubmissionOne, []byte("payload"))
}

func TestSubmitDropRejectsOversizeWithoutConsumingID(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_oversize", "drop_oversize", "pick_oversize", dropTestNow())
	dp.MaxBytes = 4
	insertHTTPDropPoint(t, repo, dp)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_oversize", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("12345")))
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize = %d %s", recorder.Code, recorder.Body.String())
	}
	if _, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
		t.Fatalf("oversize row error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(blobs.DropDir(dp.ID), httpSubmissionOne)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize blob directory error = %v", err)
	}
}

func TestSubmitDropEnforcesMultipartFraming(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []testMultipartPart
	}{
		{name: "payload first", parts: []testMultipartPart{{payloadPartName, octetContentType, []byte("payload")}, {envelopePartName, jsonContentType, []byte(testEnvelopeJSON())}}},
		{name: "extra part", parts: []testMultipartPart{{envelopePartName, jsonContentType, []byte(testEnvelopeJSON())}, {payloadPartName, octetContentType, []byte("payload")}, {"extra", octetContentType, []byte("extra")}}},
		{name: "large envelope", parts: []testMultipartPart{{envelopePartName, jsonContentType, bytes.Repeat([]byte(" "), maxEnvelopeBytes+1)}, {payloadPartName, octetContentType, []byte("payload")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, handler := newDropTestHandler(t)
			dp := testHTTPDropPoint(t, "dp_framing", "drop_framing", "pick_framing", dropTestNow())
			insertHTTPDropPoint(t, repo, dp)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequestWithParts(t, submissionPath("drop_framing", httpSubmissionOne), tc.parts))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestSubmitDropAuthorizesTokenAndValidatesSubmissionID(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_auth", "drop_auth", "pick_auth", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	for _, path := range []string{
		submissionPath("drop_unknown", httpSubmissionOne),
		submissionPath("pick_auth", httpSubmissionOne),
		submissionPath("drop_auth", "sub_too-short"),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, path, []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("path %q = %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestSubmitDropMapsStorageFailuresAndLeavesSessionOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "disk full", err: syscall.ENOSPC, want: http.StatusInsufficientStorage},
		{name: "temporary", err: syscall.EAGAIN, want: http.StatusServiceUnavailable},
		{name: "internal", err: errors.New("fsync failed"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, blobs, _ := newDropTestHandler(t)
			dp := testHTTPDropPoint(t, "dp_storage", "drop_storage", "pick_storage", dropTestNow())
			insertHTTPDropPoint(t, repo, dp)
			var logs bytes.Buffer
			handler := dropHandler(repo, &writeErrorBlobStore{BlobStore: blobs, err: tc.err}, &logs)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_storage", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
			if recorder.Code != tc.want {
				t.Fatalf("response = %d %s, want %d", recorder.Code, recorder.Body.String(), tc.want)
			}
			if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), dp.DropTokenHash, dropTestNow()); err != nil {
				t.Fatalf("session no longer open: %v", err)
			}
			if !strings.Contains(logs.String(), "event=submission.failed") || strings.Contains(logs.String(), "drop_storage") {
				t.Fatalf("unsafe or missing structured log: %s", logs.String())
			}
		})
	}
}

func TestSubmitDropHandlesCommitFailureAndAmbiguousSuccess(t *testing.T) {
	t.Run("commit failure", func(t *testing.T) {
		repo, blobs, _ := newDropTestHandler(t)
		dp := testHTTPDropPoint(t, "dp_commit", "drop_commit", "pick_commit", dropTestNow())
		insertHTTPDropPoint(t, repo, dp)
		handler := dropHandler(&repositoryOverride{Repository: repo, commitErr: errors.New("commit failed")}, blobs, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_commit", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
		}
		if _, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne); !errors.Is(err, droppoint.ErrSubmissionNotFound) {
			t.Fatalf("submission row error = %v", err)
		}
	})

	t.Run("ambiguous success", func(t *testing.T) {
		repo, blobs, _ := newDropTestHandler(t)
		dp := testHTTPDropPoint(t, "dp_ambiguous", "drop_ambiguous", "pick_ambiguous", dropTestNow())
		insertHTTPDropPoint(t, repo, dp)
		handler := dropHandler(&repositoryOverride{Repository: repo, errorAfterCommit: true, rejectCommitContextLookup: true}, blobs, nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_ambiguous", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
		if recorder.Code != http.StatusOK {
			t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
		}
		stored, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
		if err != nil {
			t.Fatal(err)
		}
		if got := readBlobPath(t, blobs, stored.PayloadPath); string(got) != "payload" {
			t.Fatalf("payload after ambiguous commit = %q", got)
		}
	})
}

func TestConcurrentAttemptsForOneIDCommitAtMostOneSubmission(t *testing.T) {
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
			handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_race", httpSubmissionOne), []byte(testEnvelopeJSON()), []byte("payload")))
			statuses <- recorder.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK && status != http.StatusConflict {
			t.Fatalf("unexpected concurrent status %d", status)
		}
	}
	ready, err := repo.ListReadySubmissions(context.Background(), dp.ID)
	if err != nil || len(ready) != 1 || ready[0].ID != httpSubmissionOne {
		t.Fatalf("ready = %+v, err=%v", ready, err)
	}
}

func TestDropRequestSizeLimitRejectsOverflow(t *testing.T) {
	if _, err := dropRequestSizeLimit(math.MaxInt64); err == nil {
		t.Fatal("accepted overflowing payload limit")
	}
	got, err := dropRequestSizeLimit(1024)
	if err != nil || got != int64(1024+maxEnvelopeBytes+multipartOverhead) {
		t.Fatalf("limit = %d, err=%v", got, err)
	}
}

type repositoryOverride struct {
	Repository
	commitErr                 error
	errorAfterCommit          bool
	rejectCommitContextLookup bool
	commitContext             context.Context
}

func (r *repositoryOverride) CommitSubmission(ctx context.Context, dropPointID, submissionID string, result droppoint.CommitSubmissionResult, now time.Time) error {
	r.commitContext = ctx
	if r.commitErr != nil {
		return r.commitErr
	}
	if err := r.Repository.CommitSubmission(ctx, dropPointID, submissionID, result, now); err != nil {
		return err
	}
	if r.errorAfterCommit {
		return errors.New("injected ambiguous commit result")
	}
	return nil
}

func (r *repositoryOverride) FindSubmission(ctx context.Context, dropPointID, submissionID string) (*droppoint.Submission, error) {
	if r.rejectCommitContextLookup && ctx == r.commitContext {
		return nil, context.DeadlineExceeded
	}
	return r.Repository.FindSubmission(ctx, dropPointID, submissionID)
}

type writeErrorBlobStore struct {
	BlobStore
	err error
}

func (s *writeErrorBlobStore) WriteSubmission(context.Context, string, string, []byte, io.Reader, int64) (droppoint.CommitSubmissionResult, error) {
	return droppoint.CommitSubmissionResult{}, s.err
}

func dropHandler(repository Repository, blobs BlobStore, logs *bytes.Buffer) http.Handler {
	logger := log.New(io.Discard, "", 0)
	if logs != nil {
		logger = log.New(logs, "", 0)
	}
	return NewRouterWithDependencies(Dependencies{Config: config.Default(), Repository: repository, BlobStore: blobs, Logger: logger, Now: dropTestNow})
}

func newDropTestHandler(t *testing.T) (*store.Repository, *blobstore.Store, http.Handler) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := store.NewRepository(db.SQLDB())
	blobs := blobstore.New(dataDir)
	return repo, blobs, NewRouterWithDependencies(Dependencies{Config: config.Default(), Repository: repo, BlobStore: blobs, Now: dropTestNow})
}

func submitDrop(t *testing.T, handler http.Handler, dropToken, submissionID string, payload []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath(dropToken, submissionID), []byte(testEnvelopeJSON()), payload))
	if recorder.Code != http.StatusOK {
		t.Fatalf("submit %s = %d %s", submissionID, recorder.Code, recorder.Body.String())
	}
}

func submissionPath(dropToken, submissionID string) string {
	return "/api/drops/" + dropToken + "/submissions/" + submissionID
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
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func multipartDropRequest(t *testing.T, path string, envelope, payload []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	mustWritePart(t, writer, envelopePartName, jsonContentType, envelope)
	mustWritePart(t, writer, payloadPartName, octetContentType, payload)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func mustWritePart(t *testing.T, writer *multipart.Writer, name, contentType string, data []byte) {
	t.Helper()
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+name+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
}

func testEnvelopeJSON() string {
	return `{"protocol_version":2,"key_agreement":"` + cryptoenv.KeyAgreement + `","sender_ephemeral_public_key":"` + cryptoenv.EncodeBase64URL(make([]byte, 32)) + `","metadata_nonce":"` + cryptoenv.EncodeBase64URL(make([]byte, 12)) + `","payload_nonce":"` + cryptoenv.EncodeBase64URL(make([]byte, 12)) + `","encrypted_metadata":"` + cryptoenv.EncodeBase64URL(make([]byte, 16)) + `"}`
}

func readBlobPath(t *testing.T, blobs *blobstore.Store, relative string) []byte {
	t.Helper()
	path, err := blobs.Path(relative)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func dropTestNow() time.Time {
	return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
}
