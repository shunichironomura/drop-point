// Package config defines the DropPoint relay configuration, its built-in
// defaults, and validation rules.
//
// Configuration belongs to the imperative shell: it is loaded once at startup
// from a JSON file (or from defaults) and validated before any other component
// runs. Keeping validation here means the rest of the service can assume a
// well-formed Config.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
)

// Built-in defaults. They are chosen so the binary can start with no
// configuration file for local development.
//
// Production deployments SHOULD set an absolute data_dir such as
// /var/lib/drop-point (see SPEC §8), a real base_url, and at least one API
// token. The default data_dir is the repository-local .data directory, matching
// the runtime-data convention documented in CODE_REVIEW_ORDER.md.
const (
	DefaultListenAddr          = "127.0.0.1:8080"
	DefaultBaseURL             = "http://127.0.0.1:8080"
	DefaultDataDir             = ".data"
	DefaultTTLSeconds          = 600        // 10 minutes
	DefaultMaxTTLSeconds       = 900        // 15 minutes
	DefaultMaxBytes            = 52_428_800 // 50 MiB
	DefaultMaxActiveDropPoints = 3
)

// Config is the full relay configuration. JSON field names use snake_case to
// match SPEC §9.
type Config struct {
	ListenAddr string `json:"listen_addr"`
	BaseURL    string `json:"base_url"`
	DataDir    string `json:"data_dir"`

	DefaultTTLSeconds int `json:"default_ttl_seconds"`
	MaxTTLSeconds     int `json:"max_ttl_seconds"`

	DefaultMaxBytes int64 `json:"default_max_bytes"`
	MaxBytes        int64 `json:"max_bytes"`

	DefaultMaxActiveDropPoints int `json:"default_max_active_drop_points"`

	APITokens []APIToken `json:"api_tokens"`
}

// APIToken is one configured receiver credential. Only the hash of the secret is
// stored; plaintext API tokens MUST NOT appear in configuration (SPEC §9).
type APIToken struct {
	ID         string `json:"id"`
	SecretHash string `json:"secret_hash"`
	Enabled    bool   `json:"enabled"`
	// MaxActiveDropPoints overrides DefaultMaxActiveDropPoints for this token
	// when non-nil. A nil value means "use the configured default".
	MaxActiveDropPoints *int `json:"max_active_drop_points,omitempty"`
}

// Default returns a Config populated with the built-in defaults and no API
// tokens. The returned Config is valid.
func Default() Config {
	return Config{
		ListenAddr:                 DefaultListenAddr,
		BaseURL:                    DefaultBaseURL,
		DataDir:                    DefaultDataDir,
		DefaultTTLSeconds:          DefaultTTLSeconds,
		MaxTTLSeconds:              DefaultMaxTTLSeconds,
		DefaultMaxBytes:            DefaultMaxBytes,
		MaxBytes:                   DefaultMaxBytes,
		DefaultMaxActiveDropPoints: DefaultMaxActiveDropPoints,
	}
}

// Load reads and validates configuration from path. When path is empty the
// built-in defaults are returned (after validation). Fields absent from the JSON
// file retain their default values, and unknown fields are rejected so operator
// typos surface immediately.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := unmarshalStrict(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %q: %w", path, err)
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

// unmarshalStrict decodes JSON into v, rejecting unknown fields and trailing
// data after the top-level object.
func unmarshalStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected trailing data after JSON object")
	}
	return nil
}

var (
	apiTokenIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	secretHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Validate checks all invariants the rest of the service relies on. It returns
// the first problem found.
func (c Config) Validate() error {
	if c.ListenAddr == "" {
		return fmt.Errorf("listen_addr must not be empty")
	}
	if err := validateBaseURL(c.BaseURL); err != nil {
		return err
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir must not be empty")
	}

	if c.DefaultTTLSeconds <= 0 {
		return fmt.Errorf("default_ttl_seconds must be positive, got %d", c.DefaultTTLSeconds)
	}
	if c.MaxTTLSeconds <= 0 {
		return fmt.Errorf("max_ttl_seconds must be positive, got %d", c.MaxTTLSeconds)
	}
	if c.DefaultTTLSeconds > c.MaxTTLSeconds {
		return fmt.Errorf("default_ttl_seconds (%d) must not exceed max_ttl_seconds (%d)",
			c.DefaultTTLSeconds, c.MaxTTLSeconds)
	}

	if c.DefaultMaxBytes <= 0 {
		return fmt.Errorf("default_max_bytes must be positive, got %d", c.DefaultMaxBytes)
	}
	if c.MaxBytes <= 0 {
		return fmt.Errorf("max_bytes must be positive, got %d", c.MaxBytes)
	}
	if c.DefaultMaxBytes > c.MaxBytes {
		return fmt.Errorf("default_max_bytes (%d) must not exceed max_bytes (%d)",
			c.DefaultMaxBytes, c.MaxBytes)
	}

	if c.DefaultMaxActiveDropPoints <= 0 {
		return fmt.Errorf("default_max_active_drop_points must be positive, got %d", c.DefaultMaxActiveDropPoints)
	}

	seen := make(map[string]struct{}, len(c.APITokens))
	for i, t := range c.APITokens {
		if !apiTokenIDPattern.MatchString(t.ID) {
			return fmt.Errorf("api_tokens[%d].id %q is empty or contains characters outside [A-Za-z0-9._-]", i, t.ID)
		}
		if _, dup := seen[t.ID]; dup {
			return fmt.Errorf("api_tokens[%d].id %q is duplicated", i, t.ID)
		}
		seen[t.ID] = struct{}{}
		if !secretHashPattern.MatchString(t.SecretHash) {
			return fmt.Errorf("api_tokens[%d].secret_hash must be of the form sha256:<64 lowercase hex chars>", i)
		}
		if t.MaxActiveDropPoints != nil && *t.MaxActiveDropPoints <= 0 {
			return fmt.Errorf("api_tokens[%d].max_active_drop_points must be positive when set, got %d", i, *t.MaxActiveDropPoints)
		}
	}
	return nil
}

// validateBaseURL enforces SPEC §9: base_url MUST include scheme and host and
// MUST NOT include query or fragment components.
func validateBaseURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("base_url must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url must use the http or https scheme, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("base_url must include a host")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return fmt.Errorf("base_url must not include a query component")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return fmt.Errorf("base_url must not include a fragment component")
	}
	return nil
}
