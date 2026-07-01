package httpapi

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServeDropPageHasSecurityHeadersAndCopy(t *testing.T) {
	handler := NewRouter(log.New(&bytes.Buffer{}, "", 0))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/drop/drop_secret", nil)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{"Drop files", "Choose files", "Expires in", "Or drag files here", "Selected files", "Drop encrypted files"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
	if got := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "script-src 'self'") {
		t.Fatalf("CSP = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestDropAssetsAreSameOriginOnlyAndNoThirdPartyScripts(t *testing.T) {
	handler := NewRouter(log.New(&bytes.Buffer{}, "", 0))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/drop/drop_secret", nil)
	handler.ServeHTTP(recorder, request)
	body := recorder.Body.String()
	if strings.Contains(body, "https://") || strings.Contains(body, "http://") {
		t.Fatalf("drop page references third-party URLs: %s", body)
	}

	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/drop-assets/app.js", nil)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("app.js status = %d", recorder.Code)
	}
	app := recorder.Body.String()
	for _, want := range []string{"window.isSecureContext", "X25519", "#", "FormData", "dataTransfer", "handleDroppedFiles", "formatRemainingTime", "updateSelectedFiles", "Ready for pickup"} {
		if !strings.Contains(app, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	if strings.Contains(app, "localStorage") || strings.Contains(app, "filename=") {
		t.Fatalf("app.js appears to leak plaintext metadata: %s", app)
	}
}

func TestDropPageRequestLogRedactsDropToken(t *testing.T) {
	var logs bytes.Buffer
	handler := NewRouter(log.New(&logs, "", 0))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/drop/drop_super_secret", nil)
	handler.ServeHTTP(recorder, request)

	if strings.Contains(logs.String(), "drop_super_secret") {
		t.Fatalf("logs leaked drop token: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "/drop/:drop_token") {
		t.Fatalf("logs did not contain redacted path: %s", logs.String())
	}
}
