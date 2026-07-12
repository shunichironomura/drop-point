package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/shunichironomura/droppoint/internal/token"
)

func TestTokenGenerateCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"token", "generate"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token generate code = %d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "api_token: "+token.APITokenPrefix) {
		t.Fatalf("output missing api token: %s", output)
	}
	if !strings.Contains(output, "secret_hash: sha256:") {
		t.Fatalf("output missing hash: %s", output)
	}
}

func TestTokenManagementCommands(t *testing.T) {
	configPath := writeTestConfig(t)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"token", "add", "--config", configPath, "--id", "alice-laptop", "--max-active", "5"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token add code = %d stderr=%s", code, stderr.String())
	}
	plaintext := extractPrintedAPIToken(t, stdout.String())
	if !strings.Contains(stdout.String(), "id: alice-laptop") {
		t.Fatalf("token add output = %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "event=api_token.added") || strings.Contains(stderr.String(), plaintext) {
		t.Fatalf("token add stderr should log event without plaintext token: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"token", "list", "--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token list code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "id=alice-laptop enabled=true max_active_drop_points=5") {
		t.Fatalf("token list output = %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"token", "disable", "--config", configPath, "--id", "alice-laptop"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token disable code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "event=api_token.disabled") {
		t.Fatalf("token disable stderr missing event: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"token", "list", "--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token list after disable code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "id=alice-laptop enabled=false max_active_drop_points=5") {
		t.Fatalf("token list after disable output = %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"token", "remove", "--config", configPath, "--id", "alice-laptop"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token remove code = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "event=api_token.removed") {
		t.Fatalf("token remove stderr missing event: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"token", "list", "--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run token list after remove code = %d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "alice-laptop") {
		t.Fatalf("token list after remove output = %s", stdout.String())
	}
}

func TestTokenGenerateReportsOutputFailure(t *testing.T) {
	var stderr bytes.Buffer
	code := run([]string{"token", "generate"}, errorWriter{}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "token output error") {
		t.Fatalf("code = %d stderr=%s, want output failure", code, stderr.String())
	}
}

func TestTokenAddRemovesRowWhenOnlyCopyCannotBePrinted(t *testing.T) {
	configPath := writeTestConfig(t)
	var stderr bytes.Buffer
	code := run([]string{"token", "add", "--config", configPath, "--id", "undelivered"}, errorWriter{}, &stderr)
	if code != 1 {
		t.Fatalf("token add code = %d stderr=%s, want 1", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "event=api_token.add_output_failed") || !strings.Contains(stderr.String(), "token row removed") {
		t.Fatalf("token add stderr = %s", stderr.String())
	}

	var stdout bytes.Buffer
	stderr.Reset()
	code = run([]string{"token", "list", "--config", configPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("token list code = %d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "undelivered") {
		t.Fatalf("failed token row remains: %s", stdout.String())
	}
}

func TestTokenListAndHelpReportOutputFailures(t *testing.T) {
	configPath := writeTestConfig(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"token", "add", "--config", configPath, "--id", "listed"}, &stdout, &stderr); code != 0 {
		t.Fatalf("token add code = %d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"token", "list", "--config", configPath}, errorWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "token list output error") {
		t.Fatalf("token list code = %d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"help"}, errorWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "write help output") {
		t.Fatalf("help code = %d stderr=%s", code, stderr.String())
	}
}

func TestTokenGenerateRejectsExtraArguments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := run([]string{"token", "generate", "extra"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run token generate extra code = %d", code)
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("injected output failure")
}

func extractPrintedAPIToken(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if tokenValue, ok := strings.CutPrefix(line, "api_token: "); ok {
			if !strings.HasPrefix(tokenValue, token.APITokenPrefix) {
				t.Fatalf("api token output has wrong prefix: %s", output)
			}
			return tokenValue
		}
	}
	t.Fatalf("output missing API token: %s", output)
	return ""
}

func writeTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	dataDir := filepath.Join(dir, "data")
	body := `{"base_url":"https://drop.example.com","data_dir":` + strconv.Quote(dataDir) + `}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
