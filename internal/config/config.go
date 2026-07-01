package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	DefaultListenAddr          = "127.0.0.1:8080"
	DefaultBaseURL             = "http://127.0.0.1:8080"
	DefaultDataDir             = ".data/drop-point"
	CanonicalSystemDataDir     = "/var/lib/drop-point"
	DefaultTTLSeconds          = 600
	DefaultMaxTTLSeconds       = 900
	DefaultMaxBytes            = 52_428_800
	DefaultMaxActiveDropPoints = 3
	SecretHashSchemeSHA256     = "sha256"
	secretHashPrefixSHA256     = SecretHashSchemeSHA256 + ":"
	sha256HexEncodedByteLength = 64
)

const (
	EnvListenAddr                 = "DROP_POINT_LISTEN_ADDR"
	EnvBaseURL                    = "DROP_POINT_BASE_URL"
	EnvDataDir                    = "DROP_POINT_DATA_DIR"
	EnvDefaultTTLSeconds          = "DROP_POINT_DEFAULT_TTL_SECONDS"
	EnvMaxTTLSeconds              = "DROP_POINT_MAX_TTL_SECONDS"
	EnvDefaultMaxBytes            = "DROP_POINT_DEFAULT_MAX_BYTES"
	EnvMaxBytes                   = "DROP_POINT_MAX_BYTES"
	EnvDefaultMaxActiveDropPoints = "DROP_POINT_DEFAULT_MAX_ACTIVE_DROP_POINTS"
	EnvAPITokensJSON              = "DROP_POINT_API_TOKENS_JSON"
)

var configEnvironmentVariables = []string{
	EnvListenAddr,
	EnvBaseURL,
	EnvDataDir,
	EnvDefaultTTLSeconds,
	EnvMaxTTLSeconds,
	EnvDefaultMaxBytes,
	EnvMaxBytes,
	EnvDefaultMaxActiveDropPoints,
	EnvAPITokensJSON,
}

var sha256SecretHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Config is the JSON configuration surface for the DropPoint relay.
type Config struct {
	ListenAddr                 string     `json:"listen_addr"`
	BaseURL                    string     `json:"base_url"`
	DataDir                    string     `json:"data_dir"`
	DefaultTTLSeconds          int        `json:"default_ttl_seconds"`
	MaxTTLSeconds              int        `json:"max_ttl_seconds"`
	DefaultMaxBytes            int64      `json:"default_max_bytes"`
	MaxBytes                   int64      `json:"max_bytes"`
	DefaultMaxActiveDropPoints int        `json:"default_max_active_drop_points"`
	APITokens                  []APIToken `json:"api_tokens"`
}

// APIToken describes one configured receiver API token. SecretHash is the only
// accepted stored token material.
type APIToken struct {
	ID                  string `json:"id"`
	SecretHash          string `json:"secret_hash"`
	Enabled             bool   `json:"enabled"`
	MaxActiveDropPoints *int   `json:"max_active_drop_points,omitempty"`
}

// Default returns a complete development-safe configuration. Packaged system
// deployments should override DataDir with CanonicalSystemDataDir.
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
		APITokens:                  nil,
	}
}

// Load reads a JSON configuration file, overlaying it onto Default. Environment
// variables then override the file/default values. An empty path uses only the
// defaults plus environment overrides.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return Config{}, fmt.Errorf("open config %q: %w", path, err)
		}
		defer file.Close()

		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("decode config %q: %w", path, err)
		}

		var extra any
		switch err := decoder.Decode(&extra); {
		case errors.Is(err, io.EOF):
		case err != nil:
			return Config{}, fmt.Errorf("decode config %q: %w", path, err)
		default:
			return Config{}, fmt.Errorf("decode config %q: trailing JSON value", path)
		}
	}

	envConfig, err := ApplyEnvironmentOverrides(cfg, os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	if err := envConfig.Validate(); err != nil {
		return Config{}, err
	}
	return envConfig, nil
}

// ApplyEnvironmentOverrides returns cfg with DROP_POINT_* environment values
// applied. The lookup function matches os.LookupEnv and keeps parsing testable.
func ApplyEnvironmentOverrides(cfg Config, lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return cfg, nil
	}
	overrides := []struct {
		name  string
		apply func(string) error
	}{
		{name: EnvListenAddr, apply: func(value string) error { cfg.ListenAddr = value; return nil }},
		{name: EnvBaseURL, apply: func(value string) error { cfg.BaseURL = value; return nil }},
		{name: EnvDataDir, apply: func(value string) error { cfg.DataDir = value; return nil }},
		{name: EnvDefaultTTLSeconds, apply: func(value string) error {
			parsed, err := parseEnvInt(EnvDefaultTTLSeconds, value)
			cfg.DefaultTTLSeconds = parsed
			return err
		}},
		{name: EnvMaxTTLSeconds, apply: func(value string) error {
			parsed, err := parseEnvInt(EnvMaxTTLSeconds, value)
			cfg.MaxTTLSeconds = parsed
			return err
		}},
		{name: EnvDefaultMaxBytes, apply: func(value string) error {
			parsed, err := parseEnvInt64(EnvDefaultMaxBytes, value)
			cfg.DefaultMaxBytes = parsed
			return err
		}},
		{name: EnvMaxBytes, apply: func(value string) error {
			parsed, err := parseEnvInt64(EnvMaxBytes, value)
			cfg.MaxBytes = parsed
			return err
		}},
		{name: EnvDefaultMaxActiveDropPoints, apply: func(value string) error {
			parsed, err := parseEnvInt(EnvDefaultMaxActiveDropPoints, value)
			cfg.DefaultMaxActiveDropPoints = parsed
			return err
		}},
		{name: EnvAPITokensJSON, apply: func(value string) error {
			parsed, err := parseAPITokensJSON(value)
			cfg.APITokens = parsed
			return err
		}},
	}

	var errs []error
	for _, override := range overrides {
		value, ok := lookup(override.name)
		if !ok {
			continue
		}
		if err := override.apply(value); err != nil {
			errs = append(errs, err)
		}
	}
	return cfg, errors.Join(errs...)
}

// Validate checks the semantic configuration constraints that are independent
// from external services.
func (c Config) Validate() error {
	var errs []error

	if c.ListenAddr == "" {
		errs = append(errs, errors.New("listen_addr must not be empty"))
	}
	if err := validateBaseURL(c.BaseURL); err != nil {
		errs = append(errs, err)
	}
	if c.DataDir == "" {
		errs = append(errs, errors.New("data_dir must not be empty"))
	}
	if c.DefaultTTLSeconds <= 0 {
		errs = append(errs, errors.New("default_ttl_seconds must be positive"))
	}
	if c.MaxTTLSeconds <= 0 {
		errs = append(errs, errors.New("max_ttl_seconds must be positive"))
	}
	if c.DefaultTTLSeconds > c.MaxTTLSeconds {
		errs = append(errs, errors.New("default_ttl_seconds must not exceed max_ttl_seconds"))
	}
	if c.DefaultMaxBytes <= 0 {
		errs = append(errs, errors.New("default_max_bytes must be positive"))
	}
	if c.MaxBytes <= 0 {
		errs = append(errs, errors.New("max_bytes must be positive"))
	}
	if c.DefaultMaxBytes > c.MaxBytes {
		errs = append(errs, errors.New("default_max_bytes must not exceed max_bytes"))
	}
	if c.DefaultMaxActiveDropPoints <= 0 {
		errs = append(errs, errors.New("default_max_active_drop_points must be positive"))
	}

	seenTokenIDs := make(map[string]struct{}, len(c.APITokens))
	for i, token := range c.APITokens {
		if token.ID == "" {
			errs = append(errs, fmt.Errorf("api_tokens[%d].id must not be empty", i))
		} else if _, ok := seenTokenIDs[token.ID]; ok {
			errs = append(errs, fmt.Errorf("api_tokens[%d].id %q is duplicated", i, token.ID))
		} else {
			seenTokenIDs[token.ID] = struct{}{}
		}
		if !ValidSecretHash(token.SecretHash) {
			errs = append(errs, fmt.Errorf("api_tokens[%d].secret_hash must use sha256:<lowercase-hex-sha256>", i))
		}
		if token.MaxActiveDropPoints != nil && *token.MaxActiveDropPoints <= 0 {
			errs = append(errs, fmt.Errorf("api_tokens[%d].max_active_drop_points must be positive when set", i))
		}
	}

	return errors.Join(errs...)
}

// ValidSecretHash reports whether s matches sha256:<lowercase-hex-sha256>.
func ValidSecretHash(s string) bool {
	if len(s) != len(secretHashPrefixSHA256)+sha256HexEncodedByteLength {
		return false
	}
	return sha256SecretHashPattern.MatchString(s)
}

func parseEnvInt(name string, value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func parseEnvInt64(name string, value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}

func parseAPITokensJSON(value string) ([]APIToken, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var tokens []APIToken
	if err := decoder.Decode(&tokens); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of API tokens: %w", EnvAPITokensJSON, err)
	}
	var extra any
	switch err := decoder.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return nil, fmt.Errorf("%s must be a JSON array of API tokens: %w", EnvAPITokensJSON, err)
	default:
		return nil, fmt.Errorf("%s must contain one JSON value", EnvAPITokensJSON)
	}
	return tokens, nil
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url is invalid: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("base_url must include scheme and host")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("base_url must not include query or fragment")
	}
	if parsed.User != nil {
		return errors.New("base_url must not include user info")
	}
	return nil
}
