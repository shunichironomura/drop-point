package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shunichironomura/drop-point/internal/droppoint"
	"github.com/shunichironomura/drop-point/internal/token"
)

func TestStatusRequiresOwnPickupToken(t *testing.T) {
	repo, handler := newCreateTestHandler(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp1 := testHTTPDropPoint(t, "dp_status_one", "drop_one", "pick_one", now)
	dp2 := testHTTPDropPoint(t, "dp_status_two", "drop_two", "pick_two", now)
	for _, dp := range []droppoint.DropPoint{dp1, dp2} {
		if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
			t.Fatalf("CreateDropPoint %s: %v", dp.ID, err)
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp1.ID+"/status", nil)
	request.Header.Set("Authorization", "Bearer pick_one")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("own pickup status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"open"`) || !strings.Contains(recorder.Body.String(), `"display_name":"calm-otter"`) {
		t.Fatalf("status body = %s", recorder.Body.String())
	}

	for name, bearer := range map[string]string{
		"other pickup token": "Bearer pick_two",
		"drop token":         "Bearer drop_one",
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp1.ID+"/status", nil)
			request.Header.Set("Authorization", bearer)
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestStatusReportsExpiredConsistently(t *testing.T) {
	repo, handler := newCreateTestHandler(t)
	now := time.Date(2026, 7, 1, 11, 40, 0, 0, time.UTC)
	dp := testHTTPDropPoint(t, "dp_expired_status", "drop_expired", "pick_expired", now)
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/drop-points/"+dp.ID+"/status", nil)
	request.Header.Set("Authorization", "Bearer pick_expired")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"status":"expired"`) {
		t.Fatalf("expired body = %s", recorder.Body.String())
	}
}

func TestCloseIsIdempotentAndPreventsDrops(t *testing.T) {
	repo, handler := newCreateTestHandler(t)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := testHTTPDropPoint(t, "dp_close", "drop_close", "pick_close", now)
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}

	for i := range 2 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodDelete, "/api/drop-points/"+dp.ID, nil)
		request.Header.Set("Authorization", "Bearer pick_close")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("close #%d status = %d body=%s", i+1, recorder.Code, recorder.Body.String())
		}
	}
	if _, err := repo.FindOpenDropPointByDropTokenHash(context.Background(), token.HashSecret("drop_close"), now); !errors.Is(err, droppoint.ErrDropPointClosed) {
		t.Fatalf("drop after close err = %v, want ErrDropPointClosed", err)
	}
}

func TestCloseDeletesPayloadFilesWhenPresent(t *testing.T) {
	fake := &fakeCloseBlobStore{}
	repo, handler := newCreateTestHandlerWithBlob(t, fake)
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	dp := testHTTPDropPoint(t, "dp_close_ready", "drop_ready", "pick_ready", now)
	if err := repo.CreateDropPoint(context.Background(), dp); err != nil {
		t.Fatalf("CreateDropPoint: %v", err)
	}
	if err := repo.BeginReceivingDrop(context.Background(), dp.ID, now); err != nil {
		t.Fatalf("BeginReceivingDrop: %v", err)
	}
	if err := repo.CommitReceivedDrop(context.Background(), dp.ID, droppoint.CommitDropResult{EnvelopePath: "drop-points/dp_close_ready/envelope.json", PayloadPath: "drop-points/dp_close_ready/payload.bin", EncryptedSize: 12}, now); err != nil {
		t.Fatalf("CommitReceivedDrop: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/drop-points/"+dp.ID, nil)
	request.Header.Set("Authorization", "Bearer pick_ready")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("close ready status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != dp.ID {
		t.Fatalf("deleted ids = %v, want [%s]", fake.deleted, dp.ID)
	}
	closed, err := repo.FindDropPointByID(context.Background(), dp.ID)
	if err != nil {
		t.Fatalf("FindDropPointByID: %v", err)
	}
	if closed.PayloadPath != "" || closed.EnvelopePath != "" || closed.EncryptedSize != 0 {
		t.Fatalf("file pointers were not cleared: %+v", closed)
	}
}

type fakeCloseBlobStore struct {
	deleted []string
}

func (f *fakeCloseBlobStore) WriteDrop(context.Context, string, []byte, io.Reader, int64) (droppoint.CommitDropResult, error) {
	return droppoint.CommitDropResult{}, errors.New("not implemented")
}

func (f *fakeCloseBlobStore) ReadEnvelope(context.Context, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeCloseBlobStore) OpenPayload(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeCloseBlobStore) DeleteDropPoint(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func testHTTPDropPoint(t *testing.T, id string, dropPlain string, pickupPlain string, now time.Time) droppoint.DropPoint {
	t.Helper()
	dp, err := droppoint.New(droppoint.CreateDropPointRequest{
		ID:              id,
		APITokenID:      "desktop-main",
		DisplayName:     "calm-otter",
		DropTokenHash:   token.HashSecret(dropPlain),
		PickupTokenHash: token.HashSecret(pickupPlain),
		TTL:             10 * time.Minute,
		MaxBytes:        1024,
	}, now)
	if err != nil {
		t.Fatalf("droppoint.New: %v", err)
	}
	return dp
}
