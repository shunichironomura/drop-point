package httpapi

import (
	"strings"

	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/token"
)

type authenticatedAPIToken struct {
	ID                  string
	MaxActiveDropPoints int
}

func authenticateAPIToken(cfg config.Config, authorization string) (authenticatedAPIToken, bool) {
	plaintext, ok := bearerToken(authorization)
	if !ok {
		return authenticatedAPIToken{}, false
	}
	for _, candidate := range cfg.APITokens {
		if !candidate.Enabled {
			continue
		}
		if token.VerifySecretHash(plaintext, candidate.SecretHash) {
			limit := cfg.DefaultMaxActiveDropPoints
			if candidate.MaxActiveDropPoints != nil {
				limit = *candidate.MaxActiveDropPoints
			}
			return authenticatedAPIToken{ID: candidate.ID, MaxActiveDropPoints: limit}, true
		}
	}
	return authenticatedAPIToken{}, false
}

func bearerToken(authorization string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return "", false
	}
	value := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return "", false
	}
	return value, true
}
