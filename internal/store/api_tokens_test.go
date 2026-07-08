package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shunichironomura/droppoint/internal/token"
)

func TestRepositoryAPITokenManagement(t *testing.T) {
	repo := newTestRepository(t)
	now := testNow()
	maxActive := 5
	plain := "api_test_secret"

	if err := repo.AddAPIToken(context.Background(), AddAPITokenParams{ID: "alice-laptop", SecretHash: token.HashSecret(plain), MaxActiveDropPoints: &maxActive, CreatedAt: now}); err != nil {
		t.Fatalf("AddAPIToken: %v", err)
	}
	authenticated, err := repo.FindEnabledAPITokenBySecretHash(context.Background(), token.HashSecret(plain))
	if err != nil {
		t.Fatalf("FindEnabledAPITokenBySecretHash: %v", err)
	}
	if authenticated.ID != "alice-laptop" || authenticated.MaxActiveDropPoints == nil || *authenticated.MaxActiveDropPoints != maxActive {
		t.Fatalf("authenticated token mismatch: %+v", authenticated)
	}
	if _, err := repo.FindEnabledAPITokenBySecretHash(context.Background(), token.HashSecret("wrong")); !errors.Is(err, ErrAPITokenNotFound) {
		t.Fatalf("wrong token err = %v, want ErrAPITokenNotFound", err)
	}

	listed, err := repo.ListAPITokens(context.Background())
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "alice-laptop" || !listed[0].Enabled || listed[0].DisabledAt != nil {
		t.Fatalf("listed enabled token mismatch: %+v", listed)
	}

	disabledAt := now.Add(time.Minute)
	if err := repo.DisableAPIToken(context.Background(), "alice-laptop", disabledAt); err != nil {
		t.Fatalf("DisableAPIToken: %v", err)
	}
	if _, err := repo.FindEnabledAPITokenBySecretHash(context.Background(), token.HashSecret(plain)); !errors.Is(err, ErrAPITokenNotFound) {
		t.Fatalf("disabled token auth err = %v, want ErrAPITokenNotFound", err)
	}
	listed, err = repo.ListAPITokens(context.Background())
	if err != nil {
		t.Fatalf("ListAPITokens after disable: %v", err)
	}
	if len(listed) != 1 || listed[0].Enabled || listed[0].DisabledAt == nil || !listed[0].DisabledAt.Equal(disabledAt) {
		t.Fatalf("listed disabled token mismatch: %+v", listed)
	}

	if err := repo.RemoveAPIToken(context.Background(), "alice-laptop"); err != nil {
		t.Fatalf("RemoveAPIToken: %v", err)
	}
	listed, err = repo.ListAPITokens(context.Background())
	if err != nil {
		t.Fatalf("ListAPITokens after remove: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("listed after remove = %+v, want empty", listed)
	}
	if err := repo.RemoveAPIToken(context.Background(), "alice-laptop"); !errors.Is(err, ErrAPITokenNotFound) {
		t.Fatalf("second remove err = %v, want ErrAPITokenNotFound", err)
	}
}
