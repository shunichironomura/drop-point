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
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestPickupRetrievesReadyCiphertextAndRecordsFirstPickup(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	now := dropTestNow()
	dp := testHTTPDropPoint(t, "dp_pickup", "drop_pickup", "pick_pickup", now)
	insertHTTPDropPoint(t, repo, dp)
	envelope := []byte(testEnvelopeJSON())
	payload := []byte("ciphertext")
	dropRecorder := httptest.NewRecorder()
	handler.ServeHTTP(dropRecorder, multipartDropRequest(t, "/api/drops/drop_pickup", envelope, payload))
	if dropRecorder.Code != http.StatusOK {
		t.Fatalf("drop status = %d body=%s", dropRecorder.Code, dropRecorder.Body.String())
	}

	headRecorder := httptest.NewRecorder()
	headRequest := httptest.NewRequest(http.MethodHead, "/api/drop-points/"+dp.ID+"/pickup", nil)
	headRequest.Header.Set("Authorization", "Bearer pick_pickup")
	handler.ServeHTTP(headRecorder, headRequest)
	if headRecorder.Code != http.StatusOK {
		t.Fatalf("HEAD pickup status = %d body=%s", headRecorder.Code, headRecorder.Body.String())
	}
	if headRecorder.Body.Len() != 0 {
		t.Fatalf("HEAD pickup body length = %d, want 0", headRecorder.Body.Len())
	}
	notPicked, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID after HEAD: %v", err)
	}
	if notPicked.FirstPickedUpAt != nil {
		t.Fatalf("HEAD pickup recorded first pickup: %v", notPicked.FirstPickedUpAt)
	}

	pickupRecorder := httptest.NewRecorder()
	pickupRequest := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp.ID+"/pickup", nil)
	pickupRequest.Header.Set("Authorization", "Bearer pick_pickup")
	handler.ServeHTTP(pickupRecorder, pickupRequest)
	if pickupRecorder.Code != http.StatusOK {
		t.Fatalf("pickup status = %d body=%s", pickupRecorder.Code, pickupRecorder.Body.String())
	}
	if got := pickupRecorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	gotEnvelope, gotPayload := readPickupMultipart(t, pickupRecorder)
	if !bytes.Equal(gotEnvelope, envelope) {
		t.Fatalf("envelope = %q, want %q", gotEnvelope, envelope)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload = %q, want %q", gotPayload, payload)
	}
	picked, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if picked.FirstPickedUpAt == nil {
		t.Fatal("first pickup timestamp was not recorded")
	}
	first := *picked.FirstPickedUpAt

	pickupRecorder = httptest.NewRecorder()
	pickupRequest = httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp.ID+"/pickup", nil)
	pickupRequest.Header.Set("Authorization", "Bearer pick_pickup")
	handler.ServeHTTP(pickupRecorder, pickupRequest)
	if pickupRecorder.Code != http.StatusOK {
		t.Fatalf("second pickup status = %d", pickupRecorder.Code)
	}
	pickedAgain, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID again: %v", err)
	}
	if pickedAgain.FirstPickedUpAt == nil || !pickedAgain.FirstPickedUpAt.Equal(first) {
		t.Fatalf("first pickup changed: %v -> %v", first, pickedAgain.FirstPickedUpAt)
	}
}

func TestPickupRecordsAfterFinalWriteDespiteCancellationAndClose(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupDropPoint(t, repo, handler, "dp_pickup_finalize", "drop_pickup_finalize", "pick_pickup_finalize")
	envelope := []byte(testEnvelopeJSON())
	payload := []byte("ciphertext")
	expected := httptest.NewRecorder()
	if err := writePickupMultipart(expected, envelope, bytes.NewReader(payload)); err != nil {
		t.Fatalf("write expected multipart: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := newCallbackResponseWriter(expected.Body.Len(), func() {
		cancel()
		if err := repo.CloseDropPoint(context.Background(), dp.ID, dropTestNow().Add(time.Second)); err != nil {
			t.Errorf("CloseDropPoint: %v", err)
		}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp.ID+"/pickup", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer pick_pickup_finalize")
	handler.ServeHTTP(writer, request)

	row, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != droppoint.StatusClosed || row.FirstPickedUpAt == nil {
		t.Fatalf("row after completed canceled pickup = %+v", row)
	}
}

func TestPickupRecordsAfterFinalWriteDespiteConcurrentExpiry(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupDropPoint(t, repo, handler, "dp_pickup_expiry_race", "drop_pickup_expiry_race", "pick_pickup_expiry_race")
	envelope := []byte(testEnvelopeJSON())
	payload := []byte("ciphertext")
	expected := httptest.NewRecorder()
	if err := writePickupMultipart(expected, envelope, bytes.NewReader(payload)); err != nil {
		t.Fatalf("write expected multipart: %v", err)
	}
	writer := newCallbackResponseWriter(expected.Body.Len(), func() {
		if _, err := repo.ExpireDropPoints(context.Background(), dropTestNow().Add(20*time.Minute)); err != nil {
			t.Errorf("ExpireDropPoints: %v", err)
		}
	})
	request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp.ID+"/pickup", nil)
	request.Header.Set("Authorization", "Bearer pick_pickup_expiry_race")
	handler.ServeHTTP(writer, request)

	row, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.Status != droppoint.StatusExpired || row.FirstPickedUpAt == nil {
		t.Fatalf("row after expiry race = %+v", row)
	}
}

func TestPickupDoesNotRecordPartialResponseWrite(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := readyPickupDropPoint(t, repo, handler, "dp_pickup_partial_write", "drop_pickup_partial_write", "pick_pickup_partial_write")
	writer := &callbackResponseWriter{header: make(http.Header), failAfter: 1}
	request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp.ID+"/pickup", nil)
	request.Header.Set("Authorization", "Bearer pick_pickup_partial_write")
	handler.ServeHTTP(writer, request)

	row, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if row.FirstPickedUpAt != nil {
		t.Fatalf("partial response recorded pickup: %v", row.FirstPickedUpAt)
	}
}

func TestPickupRejectsNotReadyWrongTokenClosedAndExpired(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	now := dropTestNow()
	open := testHTTPDropPoint(t, "dp_pickup_open", "drop_open", "pick_open", now)
	closed := testHTTPDropPoint(t, "dp_pickup_closed", "drop_closed", "pick_closed", now)
	expired := testHTTPDropPoint(t, "dp_pickup_expired", "drop_expired", "pick_expired", now.Add(-20*time.Minute))
	for _, dp := range []droppoint.DropPoint{open, closed, expired} {
		insertHTTPDropPoint(t, repo, dp)
	}
	if err := repo.CloseDropPoint(context.Background(), closed.ID, now); err != nil {
		t.Fatalf("CloseDropPoint: %v", err)
	}

	tests := []struct {
		name       string
		id         string
		bearer     string
		wantStatus int
	}{
		{name: "before drop", id: open.ID, bearer: "pick_open", wantStatus: http.StatusConflict},
		{name: "drop token", id: open.ID, bearer: "drop_open", wantStatus: http.StatusNotFound},
		{name: "closed", id: closed.ID, bearer: "pick_closed", wantStatus: http.StatusGone},
		{name: "expired", id: expired.ID, bearer: "pick_expired", wantStatus: http.StatusGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+tt.id+"/pickup", nil)
			request.Header.Set("Authorization", "Bearer "+tt.bearer)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tt.wantStatus)
			}
		})
	}
	if _, err := repo.AuthorizePickupToken(context.Background(), expired.ID, token.HashSecret("pick_expired"), now); err != nil {
		t.Fatalf("expired token should still authorize status row: %v", err)
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

func (w *callbackResponseWriter) Header() http.Header {
	return w.header
}

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

func readyPickupDropPoint(t *testing.T, repo *store.Repository, handler http.Handler, id string, dropToken string, pickupToken string) droppoint.DropPoint {
	t.Helper()
	dp := testHTTPDropPoint(t, id, dropToken, pickupToken, dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, "/api/drops/"+dropToken, []byte(testEnvelopeJSON()), []byte("ciphertext")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("drop status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	return dp
}

func readPickupMultipart(t *testing.T, recorder *httptest.ResponseRecorder) ([]byte, []byte) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
	if err != nil {
		t.Fatalf("ParseMediaType: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("Content-Type = %q, want multipart/mixed", mediaType)
	}
	reader := multipart.NewReader(strings.NewReader(recorder.Body.String()), params["boundary"])
	envelopePart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("NextPart envelope: %v", err)
	}
	envelope, err := io.ReadAll(envelopePart)
	if err != nil {
		t.Fatalf("ReadAll envelope: %v", err)
	}
	payloadPart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("NextPart payload: %v", err)
	}
	payload, err := io.ReadAll(payloadPart)
	if err != nil {
		t.Fatalf("ReadAll payload: %v", err)
	}
	if extra, err := reader.NextPart(); err != io.EOF {
		if extra != nil {
			_ = extra.Close()
		}
		t.Fatalf("extra part err = %v", err)
	}
	return envelope, payload
}
