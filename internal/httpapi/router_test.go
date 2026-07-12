package httpapi

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthReturnsLowInformationOK(t *testing.T) {
	handler := NewRouter(log.New(&bytes.Buffer{}, "", 0))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want low-information health JSON", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if strings.Contains(recorder.Body.String(), "drop") || strings.Contains(recorder.Body.String(), "path") {
		t.Fatalf("health body exposes operational detail: %q", recorder.Body.String())
	}
}

func TestHealthRejectsUnsupportedMethod(t *testing.T) {
	handler := NewRouter(log.New(&bytes.Buffer{}, "", 0))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/health", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}

func TestHeadIsAllowedOnGetRoutes(t *testing.T) {
	handler := NewRouter(log.New(&bytes.Buffer{}, "", 0))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodHead, "/drop/drop_secret", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("HEAD /drop status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("HEAD /drop body length = %d, want 0", recorder.Body.Len())
	}
}

func TestStatusRecorderUnwrapsOptionalInterfaces(t *testing.T) {
	underlying := &flushingResponseWriter{ResponseWriter: httptest.NewRecorder()}
	recorder := &statusRecorder{ResponseWriter: underlying}
	if err := http.NewResponseController(recorder).Flush(); err != nil {
		t.Fatalf("Flush through statusRecorder: %v", err)
	}
	if !underlying.flushed {
		t.Fatal("underlying Flusher was not called")
	}
}

func TestRequestLoggingRedactsEncodedCapabilityPaths(t *testing.T) {
	for _, path := range []string{
		"/api%2Fdrops%2Fdrop_secret",
		"/api%252Fdrops%252Fdrop_secret",
		"/drop%2Fdrop_secret",
	} {
		t.Run(path, func(t *testing.T) {
			var logs bytes.Buffer
			handler := NewRouter(log.New(&logs, "", 0))
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			handler.ServeHTTP(recorder, request)
			if strings.Contains(logs.String(), "drop_secret") {
				t.Fatalf("logs leaked encoded capability path: %s", logs.String())
			}
		})
	}
}

func TestRecoverPanics(t *testing.T) {
	var logs bytes.Buffer
	handler := RecoverPanics(log.New(&logs, "", 0), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("drop_secret")
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(logs.String(), "panic recovered") {
		t.Fatalf("logs = %q, want panic recovery line", logs.String())
	}
	if strings.Contains(logs.String(), "drop_secret") {
		t.Fatalf("panic log leaked recovered capability value: %s", logs.String())
	}
}

type flushingResponseWriter struct {
	http.ResponseWriter
	flushed bool
}

func (w *flushingResponseWriter) Flush() {
	w.flushed = true
}

func TestRedactTokenPath(t *testing.T) {
	tests := map[string]string{
		"/drop/drop_secret":               "/drop/:drop_token",
		"/api/drops/drop_secret":          "/api/drops/:drop_token",
		"/api%2Fdrops%2Fdrop_secret":      "/api/drops/:drop_token",
		"/api%252Fdrops%252Fdrop_secret":  "/api/drops/:drop_token",
		"/api%2Fdrops%2Fdrop%5Fsecret%ZZ": "/:redacted-capability-path",
		"/unmatched/drop_secret/more":     "/unmatched/:capability/more",
		"/health":                         "/health",
	}
	for input, want := range tests {
		if got := RedactTokenPath(input); got != want {
			t.Fatalf("RedactTokenPath(%q) = %q, want %q", input, got, want)
		}
	}
}
