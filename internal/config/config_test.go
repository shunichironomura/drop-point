package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default() is not valid: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:8080" {
		t.Errorf("default listen_addr = %q, want 127.0.0.1:8080", cfg.ListenAddr)
	}
}

func TestLoadEmptyPathReturnsDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	// Config has a slice field and so is not == comparable; check the scalars.
	want := Default()
	if cfg.ListenAddr != want.ListenAddr ||
		cfg.BaseURL != want.BaseURL ||
		cfg.DataDir != want.DataDir ||
		cfg.DefaultTTLSeconds != want.DefaultTTLSeconds ||
		cfg.MaxTTLSeconds != want.MaxTTLSeconds ||
		cfg.DefaultMaxBytes != want.DefaultMaxBytes ||
		cfg.MaxBytes != want.MaxBytes ||
		cfg.DefaultMaxActiveDropPoints != want.DefaultMaxActiveDropPoints {
		t.Errorf("Load(\"\") did not return defaults: %+v", cfg)
	}
	if len(cfg.APITokens) != 0 {
		t.Errorf("expected no api tokens by default, got %d", len(cfg.APITokens))
	}
}

func TestLoadFromFileOverridesAndKeepsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	contents := `{
		"listen_addr": "0.0.0.0:9000",
		"base_url": "https://drop.example.com",
		"api_tokens": [
			{"id": "desktop-main", "secret_hash": "sha256:` + hex64() + `", "enabled": true, "max_active_drop_points": 5}
		]
	}`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("listen_addr = %q, want override", cfg.ListenAddr)
	}
	if cfg.BaseURL != "https://drop.example.com" {
		t.Errorf("base_url = %q, want override", cfg.BaseURL)
	}
	// Untouched fields keep their defaults.
	if cfg.DataDir != DefaultDataDir {
		t.Errorf("data_dir = %q, want default %q", cfg.DataDir, DefaultDataDir)
	}
	if cfg.MaxTTLSeconds != DefaultMaxTTLSeconds {
		t.Errorf("max_ttl_seconds = %d, want default %d", cfg.MaxTTLSeconds, DefaultMaxTTLSeconds)
	}
	if len(cfg.APITokens) != 1 {
		t.Fatalf("got %d api tokens, want 1", len(cfg.APITokens))
	}
	tok := cfg.APITokens[0]
	if tok.ID != "desktop-main" || !tok.Enabled {
		t.Errorf("unexpected token: %+v", tok)
	}
	if tok.MaxActiveDropPoints == nil || *tok.MaxActiveDropPoints != 5 {
		t.Errorf("max_active_drop_points override not parsed: %+v", tok.MaxActiveDropPoints)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"listen_addr": "x", "bogus": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json")); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestValidateRejectsBadConfigs(t *testing.T) {
	base := Default()
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty listen_addr", func(c *Config) { c.ListenAddr = "" }},
		{"empty base_url", func(c *Config) { c.BaseURL = "" }},
		{"base_url without scheme", func(c *Config) { c.BaseURL = "drop.example.com" }},
		{"base_url wrong scheme", func(c *Config) { c.BaseURL = "ftp://drop.example.com" }},
		{"base_url with query", func(c *Config) { c.BaseURL = "https://drop.example.com?a=b" }},
		{"base_url with fragment", func(c *Config) { c.BaseURL = "https://drop.example.com#frag" }},
		{"empty data_dir", func(c *Config) { c.DataDir = "" }},
		{"non-positive default ttl", func(c *Config) { c.DefaultTTLSeconds = 0 }},
		{"non-positive max ttl", func(c *Config) { c.MaxTTLSeconds = 0 }},
		{"default ttl exceeds max", func(c *Config) { c.DefaultTTLSeconds = c.MaxTTLSeconds + 1 }},
		{"non-positive default max_bytes", func(c *Config) { c.DefaultMaxBytes = 0 }},
		{"non-positive max_bytes", func(c *Config) { c.MaxBytes = 0 }},
		{"default max_bytes exceeds max", func(c *Config) { c.DefaultMaxBytes = c.MaxBytes + 1 }},
		{"non-positive default active quota", func(c *Config) { c.DefaultMaxActiveDropPoints = 0 }},
		{"token bad id", func(c *Config) {
			c.APITokens = []APIToken{{ID: "bad id!", SecretHash: "sha256:" + hex64()}}
		}},
		{"token bad secret hash", func(c *Config) {
			c.APITokens = []APIToken{{ID: "ok", SecretHash: "plain-secret"}}
		}},
		{"token uppercase hex hash", func(c *Config) {
			c.APITokens = []APIToken{{ID: "ok", SecretHash: "sha256:" + "A" + hex64()[1:]}}
		}},
		{"duplicate token id", func(c *Config) {
			c.APITokens = []APIToken{
				{ID: "dup", SecretHash: "sha256:" + hex64()},
				{ID: "dup", SecretHash: "sha256:" + hex64()},
			}
		}},
		{"token non-positive override", func(c *Config) {
			zero := 0
			c.APITokens = []APIToken{{ID: "ok", SecretHash: "sha256:" + hex64(), MaxActiveDropPoints: &zero}}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %s", tt.name)
			}
		})
	}
}

func TestValidateAcceptsValidTokens(t *testing.T) {
	cfg := Default()
	cfg.APITokens = []APIToken{
		{ID: "desktop-main", SecretHash: "sha256:" + hex64(), Enabled: true},
		{ID: "mobile.app-2", SecretHash: "sha256:" + hex64(), Enabled: false},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

// hex64 returns a 64-character lowercase hex string for use as a fake sha256
// hash in tests.
func hex64() string {
	const s = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = s[i%len(s)]
	}
	return string(out)
}
