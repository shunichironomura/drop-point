package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

func TestStatusRequiresOwnPickupTokenAndReportsQueue(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	now := dropTestNow()
	dp := testHTTPDropPoint(t, "dp_status", "drop_status", "pick_status", now)
	other := testHTTPDropPoint(t, "dp_status_other", "drop_other", "pick_other", now)
	insertHTTPDropPoint(t, repo, dp)
	insertHTTPDropPoint(t, repo, other)
	submitDrop(t, handler, "drop_status", httpSubmissionOne, []byte("payload"))

	recorder := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+dp.ID+"/status", "pick_status")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var status dropPointStatusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Status != droppoint.StatusOpen || status.PendingSubmissions != 1 || status.PendingBytes != int64(len("payload")) || status.MaxPendingSubmissions != dp.MaxPendingSubmissions {
		t.Fatalf("status = %+v", status)
	}
	for _, bearer := range []string{"pick_other", "drop_status"} {
		got := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+dp.ID+"/status", bearer)
		if got.Code != http.StatusNotFound {
			t.Fatalf("bearer %q status = %d body=%s", bearer, got.Code, got.Body.String())
		}
	}
}

func TestListReturnsReadySubmissions(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_list", "drop_list", "pick_list", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, "drop_list", httpSubmissionOne, []byte("one"))
	submitDrop(t, handler, "drop_list", httpSubmissionTwo, []byte("two"))

	recorder := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+dp.ID+"/submissions", "pick_list")
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response listSubmissionsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Submissions) != 2 || response.Submissions[0].SubmissionID != httpSubmissionOne || response.Submissions[1].SubmissionID != httpSubmissionTwo {
		t.Fatalf("submissions = %+v", response.Submissions)
	}
}

func TestAcknowledgeDeletesOneSubmissionAndFreesQueue(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_ack", "drop_ack", "pick_ack", dropTestNow())
	dp.MaxPendingSubmissions = 1
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, "drop_ack", httpSubmissionOne, []byte("payload"))

	for range 2 {
		recorder := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+dp.ID+"/submissions/"+httpSubmissionOne, "pick_ack")
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("ack status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	acknowledged, err := repo.FindSubmission(context.Background(), dp.ID, httpSubmissionOne)
	if err != nil || acknowledged.Status != droppoint.SubmissionStatusAcknowledged || acknowledged.PayloadPath != "" || acknowledged.EnvelopePath != "" {
		t.Fatalf("acknowledged = %+v, err=%v", acknowledged, err)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID) + "/" + httpSubmissionOne); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("submission directory stat error = %v", err)
	}
	submitDrop(t, handler, "drop_ack", httpSubmissionTwo, []byte("next"))
}

func TestCloseIsIdempotentDeletesChildrenAndPreventsFurtherUse(t *testing.T) {
	repo, blobs, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_close", "drop_close", "pick_close", dropTestNow())
	insertHTTPDropPoint(t, repo, dp)
	submitDrop(t, handler, "drop_close", httpSubmissionOne, []byte("payload"))

	for range 2 {
		recorder := authorizedRequest(t, handler, http.MethodDelete, "/api/drop-points/"+dp.ID, "pick_close")
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("close status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	closed, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil || closed.Status != droppoint.StatusClosed {
		t.Fatalf("closed = %+v, err=%v", closed, err)
	}
	if _, err := os.Stat(blobs.DropDir(dp.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop directory stat error = %v", err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, multipartDropRequest(t, submissionPath("drop_close", httpSubmissionTwo), []byte(testEnvelopeJSON()), []byte("next")))
	if recorder.Code != http.StatusGone {
		t.Fatalf("submit after close = %d body=%s", recorder.Code, recorder.Body.String())
	}
	list := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+dp.ID+"/submissions", "pick_close")
	if list.Code != http.StatusGone {
		t.Fatalf("list after close = %d body=%s", list.Code, list.Body.String())
	}
}

func TestStatusReportsExpiredSession(t *testing.T) {
	repo, _, handler := newDropTestHandler(t)
	dp := testHTTPDropPoint(t, "dp_expired_status", "drop_expired", "pick_expired", dropTestNow().Add(-20*time.Minute))
	insertHTTPDropPoint(t, repo, dp)
	recorder := authorizedRequest(t, handler, http.MethodGet, "/api/drop-points/"+dp.ID+"/status", "pick_expired")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"expired"`) {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func testHTTPDropPoint(t *testing.T, id, dropPlain, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:                    id,
		APITokenID:            "desktop-main",
		DisplayName:           "calm-otter",
		DropTokenHash:         token.HashSecret(dropPlain),
		PickupTokenHash:       token.HashSecret(pickupPlain),
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
