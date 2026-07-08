package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/shunichironomura/droppoint/internal/store"
	"github.com/shunichironomura/droppoint/internal/token"
)

type authenticatedAPIToken struct {
	ID                  string
	MaxActiveDropPoints int
}

func authenticateAPIToken(ctx context.Context, repository *store.Repository, defaultMaxActiveDropPoints int, authorization string) (authenticatedAPIToken, bool, error) {
	plaintext, ok := bearerToken(authorization)
	if !ok {
		return authenticatedAPIToken{}, false, nil
	}
	apiToken, err := repository.FindEnabledAPITokenBySecretHash(ctx, token.HashSecret(plaintext))
	if errors.Is(err, store.ErrAPITokenNotFound) {
		return authenticatedAPIToken{}, false, nil
	}
	if err != nil {
		return authenticatedAPIToken{}, false, err
	}
	limit := defaultMaxActiveDropPoints
	if apiToken.MaxActiveDropPoints != nil {
		limit = *apiToken.MaxActiveDropPoints
	}
	return authenticatedAPIToken{ID: apiToken.ID, MaxActiveDropPoints: limit}, true, nil
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
