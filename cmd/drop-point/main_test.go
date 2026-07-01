package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shunichironomura/drop-point/internal/token"
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
	if !strings.Contains(output, "secret_hash: sha256:") || !strings.Contains(output, "config_entry:") {
		t.Fatalf("output missing hash/config entry: %s", output)
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
