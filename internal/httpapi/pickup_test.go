package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/blobstore"
	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
)

func TestPickupRetrievesOneReadySubmissionAndRecordsFirstPickup(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupSubmission(t, repo, handler, "dp_pickup", "drop_pickup", "pick_pickup", httpSubmissionOne, []byte("ciphertext"))
	path := pickupPath(dp.ID, httpSubmissionOne)

	head := authorizedRequest(t, handler, http.MethodHead, path, "pick_pickup")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d body=%q", head.Code, head.Body.String())
	}
	notPicked, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || notPicked.FirstPickedUpAt != nil {
		t.Fatalf("submission after HEAD = %+v, err=%v", notPicked, err)
	}

	pickup := authorizedRequest(t, handler, http.MethodGet, path, "pick_pickup")
	if pickup.Code != http.StatusOK || pickup.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("pickup = %d %s", pickup.Code, pickup.Body.String())
	}
	gotEnvelope, gotPayload := readPickupMultipart(t, pickup)
	if string(gotEnvelope) != testEnvelopeJSON() || string(gotPayload) != "ciphertext" {
		t.Fatalf("pickup envelope=%q payload=%q", gotEnvelope, gotPayload)
	}
	picked, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || picked.FirstPickedUpAt == nil {
		t.Fatalf("picked submission = %+v, err=%v", picked, err)
	}
	first := *picked.FirstPickedUpAt

	again := authorizedRequest(t, handler, http.MethodGet, path, "pick_pickup")
	if again.Code != http.StatusOK {
		t.Fatalf("repeated pickup = %d %s", again.Code, again.Body.String())
	}
	picked, err = repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || picked.FirstPickedUpAt == nil || !picked.FirstPickedUpAt.Equal(first) {
		t.Fatalf("first pickup changed: %+v, err=%v", picked, err)
	}
}

func TestPickupIsScopedToReadySubmissionAndPickupToken(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_pickup_scope", "drop_pickup_scope", "pick_pickup_scope", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	if err := repo.BeginSubmission(context.Background(), dp.ID, httpSubmissionOne, dropTestNow()); err != nil {
		t.Fatal(err)
	}
	submitDrop(t, handler, "drop_pickup_scope", httpSubmissionTwo, []byte("ready"))

	for _, tc := range []struct {
		name   string
		id     string
		bearer string
		want   int
	}{
		{name: "receiving", id: httpSubmissionOne, bearer: "pick_pickup_scope", want: http.StatusConflict},
		{name: "unknown", id: httpSubmissionThree, bearer: "pick_pickup_scope", want: http.StatusNotFound},
		{name: "wrong token", id: httpSubmissionTwo, bearer: "drop_pickup_scope", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := authorizedRequest(t, handler, http.MethodGet, pickupPath(dp.ID, tc.id), tc.bearer)
			if recorder.Code != tc.want {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestPickupUnavailableAfterAcknowledgement(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupSubmission(t, repo, handler, "dp_pickup_ack", "drop_pickup_ack", "pick_pickup_ack", httpSubmissionOne, []byte("ciphertext"))
	ack := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+dp.ID+"/submissions/"+httpSubmissionOne, "pick_pickup_ack")
	if ack.Code != http.StatusNoContent {
		t.Fatalf("ack = %d %s", ack.Code, ack.Body.String())
	}
	pickup := authorizedRequest(t, handler, http.MethodGet, pickupPath(dp.ID, httpSubmissionOne), "pick_pickup_ack")
	if pickup.Code != http.StatusGone {
		t.Fatalf("pickup after ack = %d %s", pickup.Code, pickup.Body.String())
	}
}

func TestPickupMarksOnlyCorruptSubmissionFailed(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupSubmission(t, repo, handler, "dp_pickup_corrupt", "drop_pickup_corrupt", "pick_pickup_corrupt", httpSubmissionOne, []byte("ciphertext"))
	if err := repo.ClearSubmissionFiles(context.Background(), dp.ID, httpSubmissionOne); err != nil {
		t.Fatal(err)
	}
	pickup := authorizedRequest(t, handler, http.MethodGet, pickupPath(dp.ID, httpSubmissionOne), "pick_pickup_corrupt")
	if pickup.Code != http.StatusInternalServerError {
		t.Fatalf("corrupt pickup = %d %s", pickup.Code, pickup.Body.String())
	}
	failed, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || failed.Status != droppoint.SubmissionStatusFailed || failed.FailedAt == nil {
		t.Fatalf("failed submission = %+v, err=%v", failed, err)
	}
	if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), dp.DropTokenHash, dropTestNow()); err != nil {
		t.Fatalf("parent session was failed: %v", err)
	}
	submitDrop(t, handler, "drop_pickup_corrupt", httpSubmissionTwo, []byte("next"))
}

func TestPickupRejectsCorruptStoredContentsBeforeWritingResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(t *testing.T, blobs *blobstore.Store, submission *droppoint.Submission)
	}{
		{
			name: "invalid envelope",
			mutate: func(t *testing.T, blobs *blobstore.Store, submission *droppoint.Submission) {
				path, err := blobs.Path(submission.EnvelopePath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "payload size mismatch",
			mutate: func(t *testing.T, blobs *blobstore.Store, submission *droppoint.Submission) {
				path, err := blobs.Path(submission.PayloadPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, blobs, handler := newDropTestHandler(t)
			dp := readyPickupSubmission(t, repo, handler, "dp_stored_corrupt", "drop_stored_corrupt", "pick_stored_corrupt", httpSubmissionOne, []byte("ciphertext"))
			submission, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, blobs, submission)

			pickup := authorizedRequest(t, handler, http.MethodGet, pickupPath(dp.ID, httpSubmissionOne), "pick_stored_corrupt")
			if pickup.Code != http.StatusInternalServerError {
				t.Fatalf("pickup = %d %s", pickup.Code, pickup.Body.String())
			}
			failed, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
			if err != nil || failed.Status != droppoint.SubmissionStatusFailed || failed.FirstPickedUpAt != nil {
				t.Fatalf("failed submission = %+v, err=%v", failed, err)
			}
			if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), dp.DropTokenHash, dropTestNow()); err != nil {
				t.Fatalf("parent session was failed: %v", err)
			}
		})
	}
}

func TestHeadPickupRejectsCorruptStoredContentsWithoutRecordingPickup(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := readyPickupSubmission(t, repo, handler, "dp_head_corrupt", "drop_head_corrupt", "pick_head_corrupt", httpSubmissionOne, []byte("ciphertext"))
	submission, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil {
		t.Fatal(err)
	}
	payloadPath, err := blobs.Path(submission.PayloadPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}

	head := authorizedRequest(t, handler, http.MethodHead, pickupPath(dp.ID, httpSubmissionOne), "pick_head_corrupt")
	if head.Code != http.StatusInternalServerError || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d body=%q", head.Code, head.Body.String())
	}
	failed, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || failed.Status != droppoint.SubmissionStatusFailed || failed.FirstPickedUpAt != nil {
		t.Fatalf("failed submission = %+v, err=%v", failed, err)
	}
}

func TestPickupRecordsCompletedWriteDespiteCancellationAndClose(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupSubmission(t, repo, handler, "dp_pickup_finalize", "drop_pickup_finalize", "pick_pickup_finalize", httpSubmissionOne, []byte("ciphertext"))
	expected := httptest.NewRecorder()
	if err := writePickupMultipart(expected, []byte(testEnvelopeJSON()), bytes.NewReader([]byte("ciphertext"))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := newCallbackResponseWriter(expected.Body.Len(), func() {
		cancel()
		if err := repo.CloseDropPoint(context.Background(), dp.ID, dropTestNow().Add(time.Second)); err != nil {
			t.Errorf("CloseDropPoint: %v", err)
		}
	})
	request := httptest.NewRequest(http.MethodGet, pickupPath(dp.ID, httpSubmissionOne), nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer pick_pickup_finalize")
	handler.ServeHTTP(writer, request)

	picked, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || picked.FirstPickedUpAt == nil {
		t.Fatalf("picked submission = %+v, err=%v", picked, err)
	}
}

func TestPickupDoesNotRecordPartialResponseWrite(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupSubmission(t, repo, handler, "dp_pickup_partial", "drop_pickup_partial", "pick_pickup_partial", httpSubmissionOne, []byte("ciphertext"))
	writer := &callbackResponseWriter{header: make(http.Header), failAfter: 1}
	request := httptest.NewRequest(http.MethodGet, pickupPath(dp.ID, httpSubmissionOne), nil)
	request.Header.Set("Authorization", "Bearer pick_pickup_partial")
	handler.ServeHTTP(writer, request)
	picked, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || picked.FirstPickedUpAt != nil {
		t.Fatalf("partial pickup = %+v, err=%v", picked, err)
	}
}

type callbackResponseWriter struct {
	header     http.Header
	body       bytes.Buffer
	status     int
	callbackAt int
	failAfter  int
	callback   func()
	called     bool
}

func newCallbackResponseWriter(callbackAt int, callback func()) *callbackResponseWriter {
	return &callbackResponseWriter{header: make(http.Header), callbackAt: callbackAt, failAfter: -1, callback: callback}
}

func (w *callbackResponseWriter) Header() http.Header { return w.header }

func (w *callbackResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *callbackResponseWriter) Write(data []byte) (int, error) {
	if w.failAfter >= 0 && w.body.Len()+len(data) > w.failAfter {
		return 0, errors.New("injected response write failure")
	}
	n, err := w.body.Write(data)
	if err == nil && !w.called && w.callback != nil && w.body.Len() >= w.callbackAt {
		w.called = true
		w.callback()
	}
	return n, err
}

func readyPickupSubmission(t *testing.T, repo *store.Repository, handler http.Handler, id, dropToken, pickupToken, submissionID string, payload []byte) droppoint.DropPoint {
	t.Helper()
	dp := testHTTPDropPoint(t, id, dropToken, pickupToken, dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, dropToken, submissionID, payload)
	return dp
}

func pickupPath(dropPointID, submissionID string) string {
	return "/api/drop-points/" + dropPointID + "/submissions/" + submissionID + "/pickup"
}

func readPickupMultipart(t *testing.T, recorder *httptest.ResponseRecorder) ([]byte, []byte) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
	if err != nil || mediaType != "multipart/mixed" {
		t.Fatalf("Content-Type = %q, err=%v", recorder.Header().Get("Content-Type"), err)
	}
	reader := multipart.NewReader(strings.NewReader(recorder.Body.String()), params["boundary"])
	envelopePart, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := io.ReadAll(envelopePart)
	if err != nil {
		t.Fatal(err)
	}
	payloadPart, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(payloadPart)
	if err != nil {
		t.Fatal(err)
	}
	if extra, err := reader.NextPart(); !errors.Is(err, io.EOF) {
		if extra != nil {
			_ = extra.Close()
		}
		t.Fatalf("extra multipart part: %v", err)
	}
	return envelope, payload
}
