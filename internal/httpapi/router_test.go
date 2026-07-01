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

func TestRecoverPanics(t *testing.T) {
	var logs bytes.Buffer
	handler := RecoverPanics(log.New(&logs, "", 0), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
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
}

func TestRedactTokenPath(t *testing.T) {
	tests := map[string]string{
		"/drop/drop_secret":      "/drop/:drop_token",
		"/api/drops/drop_secret": "/api/drops/:drop_token",
		"/health":                "/health",
	}
	for input, want := range tests {
		if got := RedactTokenPath(input); got != want {
			t.Fatalf("RedactTokenPath(%q) = %q, want %q", input, got, want)
		}
	}
}
