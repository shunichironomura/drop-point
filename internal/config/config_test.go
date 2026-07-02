package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
}

func TestLoadMergesFileWithDefaults(t *testing.T) {
	withCleanConfigEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "base_url": "https://drop.example.com",
  "data_dir": "/var/lib/drop-point",
  "api_tokens": [
    {
      "id": "desktop-main",
      "secret_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "enabled": true,
      "max_active_drop_points": 5
    }
  ]
}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.ListenAddr != DefaultListenAddr {
		t.Fatalf("ListenAddr = %q, want default %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.BaseURL != "https://drop.example.com" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.DataDir != CanonicalSystemDataDir {
		t.Fatalf("DataDir = %q, want %q", cfg.DataDir, CanonicalSystemDataDir)
	}
	if len(cfg.APITokens) != 1 {
		t.Fatalf("len(APITokens) = %d, want 1", len(cfg.APITokens))
	}
	if cfg.APITokens[0].MaxActiveDropPoints == nil || *cfg.APITokens[0].MaxActiveDropPoints != 5 {
		t.Fatalf("MaxActiveDropPoints = %v, want 5", cfg.APITokens[0].MaxActiveDropPoints)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	withCleanConfigEnvironment(t)
	cfg, err := Load("../../config.example.json")
	if err != nil {
		t.Fatalf("load example config: %v", err)
	}
	if cfg.DataDir != CanonicalSystemDataDir {
		t.Fatalf("example DataDir = %q, want %q", cfg.DataDir, CanonicalSystemDataDir)
	}
}

func TestLoadAppliesEnvironmentOverrides(t *testing.T) {
	withCleanConfigEnvironment(t)
	t.Setenv(EnvListenAddr, "0.0.0.0:9090")
	t.Setenv(EnvBaseURL, "https://env.drop.example.com")
	t.Setenv(EnvDataDir, "/tmp/drop-point-env")
	t.Setenv(EnvDefaultTTLSeconds, "120")
	t.Setenv(EnvMaxTTLSeconds, "300")
	t.Setenv(EnvDefaultMaxBytes, "4096")
	t.Setenv(EnvMaxBytes, "8192")
	t.Setenv(EnvDefaultMaxActiveDropPoints, "7")
	t.Setenv(EnvReadTimeoutSeconds, "30")
	t.Setenv(EnvWriteTimeoutSeconds, "40")
	t.Setenv(EnvCleanupIntervalSeconds, "50")
	t.Setenv(EnvTerminalRetentionSeconds, "60")
	t.Setenv(EnvAPITokensJSON, `[{"id":"env-token","secret_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","enabled":true,"max_active_drop_points":2}]`)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9090" || cfg.BaseURL != "https://env.drop.example.com" || cfg.DataDir != "/tmp/drop-point-env" {
		t.Fatalf("string overrides not applied: %+v", cfg)
	}
	if cfg.DefaultTTLSeconds != 120 || cfg.MaxTTLSeconds != 300 || cfg.DefaultMaxBytes != 4096 || cfg.MaxBytes != 8192 || cfg.DefaultMaxActiveDropPoints != 7 || cfg.ReadTimeoutSeconds != 30 || cfg.WriteTimeoutSeconds != 40 || cfg.CleanupIntervalSeconds != 50 || cfg.TerminalRetentionSeconds != 60 {
		t.Fatalf("numeric overrides not applied: %+v", cfg)
	}
	if len(cfg.APITokens) != 1 || cfg.APITokens[0].ID != "env-token" || cfg.APITokens[0].MaxActiveDropPoints == nil || *cfg.APITokens[0].MaxActiveDropPoints != 2 {
		t.Fatalf("api token override not applied: %+v", cfg.APITokens)
	}
}

func TestLoadRejectsInvalidEnvironmentOverride(t *testing.T) {
	withCleanConfigEnvironment(t)
	t.Setenv(EnvMaxBytes, "not-an-integer")

	_, err := Load("")
	if err == nil {
		t.Fatal("Load succeeded, want invalid env error")
	}
	if !strings.Contains(err.Error(), EnvMaxBytes) {
		t.Fatalf("Load error = %v, want %s", err, EnvMaxBytes)
	}
}

func TestValidateRejectsInvalidBaseURL(t *testing.T) {
	cfg := Default()
	cfg.BaseURL = "https://drop.example.com/?debug=true#fragment"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("Validate() error = %v, want base_url error", err)
	}
}

func TestValidateRejectsInvalidAPITokenHash(t *testing.T) {
	cfg := Default()
	cfg.APITokens = []APIToken{{
		ID:         "desktop-main",
		SecretHash: "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		Enabled:    true,
	}}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "secret_hash") {
		t.Fatalf("Validate() error = %v, want secret_hash error", err)
	}
}

func TestEnsureDataDirUsesRestrictivePermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "drop-point")

	if err := EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir() error = %v", err)
	}

	assertDirMode(t, dataDir, 0o700)
	assertDirMode(t, filepath.Join(dataDir, "drop-points"), 0o700)
}

func withCleanConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, variable := range configEnvironmentVariables {
		variable := variable
		value, ok := os.LookupEnv(variable)
		if err := os.Unsetenv(variable); err != nil {
			t.Fatalf("unset %s: %v", variable, err)
		}
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(variable, value)
				return
			}
			_ = os.Unsetenv(variable)
		})
	}
}

func assertDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", path)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode(%s) = %o, want %o", path, got, want)
	}
}
