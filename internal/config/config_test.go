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
  "data_dir": "/var/lib/droppoint"
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
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	withCleanConfigEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{
  "base_url": "https://drop.example.com",
  "unexpected_field": true
}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want unknown-field error")
	}
	if !strings.Contains(err.Error(), "unexpected_field") {
		t.Fatalf("Load error = %v, want unexpected_field error", err)
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
	t.Setenv(EnvDataDir, "/tmp/droppoint-env")
	t.Setenv(EnvDefaultTTLSeconds, "120")
	t.Setenv(EnvMaxTTLSeconds, "300")
	t.Setenv(EnvDefaultMaxBytes, "4096")
	t.Setenv(EnvMaxBytes, "8192")
	t.Setenv(EnvDefaultMaxActiveDropPoints, "7")
	t.Setenv(EnvReadTimeoutSeconds, "30")
	t.Setenv(EnvWriteTimeoutSeconds, "40")
	t.Setenv(EnvCleanupIntervalSeconds, "50")
	t.Setenv(EnvTerminalRetentionSeconds, "60")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:9090" || cfg.BaseURL != "https://env.drop.example.com" || cfg.DataDir != "/tmp/droppoint-env" {
		t.Fatalf("string overrides not applied: %+v", cfg)
	}
	if cfg.DefaultTTLSeconds != 120 || cfg.MaxTTLSeconds != 300 || cfg.DefaultMaxBytes != 4096 || cfg.MaxBytes != 8192 || cfg.DefaultMaxActiveDropPoints != 7 || cfg.ReadTimeoutSeconds != 30 || cfg.WriteTimeoutSeconds != 40 || cfg.CleanupIntervalSeconds != 50 || cfg.TerminalRetentionSeconds != 60 {
		t.Fatalf("numeric overrides not applied: %+v", cfg)
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

func TestValidateRestrictsBaseURLToHTTPRootOrigins(t *testing.T) {
	for _, valid := range []string{"http://localhost:8080", "https://drop.example.com", "https://drop.example.com/"} {
		cfg := Default()
		cfg.BaseURL = valid
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{
		"ftp://drop.example.com",
		"https://drop.example.com/prefix",
		"https://drop.example.com/?debug=true",
		"https://drop.example.com/#fragment",
		"https://user@drop.example.com",
		"https://:443",
	} {
		cfg := Default()
		cfg.BaseURL = invalid
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "base_url") {
			t.Fatalf("Validate(%q) error = %v, want base_url error", invalid, err)
		}
	}
}

func TestValidateBoundsDurationAndPayloadArithmetic(t *testing.T) {
	durationCases := []struct {
		name  string
		apply func(*Config)
	}{
		{name: "default_ttl_seconds", apply: func(cfg *Config) {
			cfg.DefaultTTLSeconds = MaxConfiguredDurationSeconds + 1
			cfg.MaxTTLSeconds = MaxConfiguredDurationSeconds + 1
		}},
		{name: "max_ttl_seconds", apply: func(cfg *Config) { cfg.MaxTTLSeconds = MaxConfiguredDurationSeconds + 1 }},
		{name: "read_timeout_seconds", apply: func(cfg *Config) { cfg.ReadTimeoutSeconds = MaxConfiguredDurationSeconds + 1 }},
		{name: "write_timeout_seconds", apply: func(cfg *Config) { cfg.WriteTimeoutSeconds = MaxConfiguredDurationSeconds + 1 }},
		{name: "cleanup_interval_seconds", apply: func(cfg *Config) { cfg.CleanupIntervalSeconds = MaxConfiguredDurationSeconds + 1 }},
		{name: "terminal_retention_seconds", apply: func(cfg *Config) { cfg.TerminalRetentionSeconds = MaxConfiguredDurationSeconds + 1 }},
	}
	for _, tt := range durationCases {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.apply(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.name) {
				t.Fatalf("Validate error = %v, want %s bound", err, tt.name)
			}
		})
	}
	for _, tt := range []struct {
		name  string
		apply func(*Config)
	}{
		{name: "default_max_bytes", apply: func(cfg *Config) {
			cfg.DefaultMaxBytes = MaxConfiguredPayloadBytes + 1
			cfg.MaxBytes = MaxConfiguredPayloadBytes + 1
		}},
		{name: "max_bytes", apply: func(cfg *Config) { cfg.MaxBytes = MaxConfiguredPayloadBytes + 1 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.apply(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), tt.name) {
				t.Fatalf("Validate error = %v, want %s bound", err, tt.name)
			}
		})
	}
}

func TestEnsureDataDirUsesRestrictivePermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "droppoint")

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
