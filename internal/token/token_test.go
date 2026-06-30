package token

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateTokensUsePrefixesAndEntropy(t *testing.T) {
	tests := map[string]func() (string, error){
		DropPointIDPrefix: GenerateDropPointID,
		DropTokenPrefix:   GenerateDropToken,
		PickupTokenPrefix: GeneratePickupToken,
		APITokenPrefix:    GenerateAPIToken,
	}

	for prefix, generate := range tests {
		value, err := generate()
		if err != nil {
			t.Fatalf("generate %s token: %v", prefix, err)
		}
		if !strings.HasPrefix(value, prefix) {
			t.Fatalf("token %q does not use prefix %q", value, prefix)
		}
		if strings.Contains(value, "=") {
			t.Fatalf("token %q contains base64 padding", value)
		}
		secret, err := DecodeSecret(value, prefix)
		if err != nil {
			t.Fatalf("decode %q: %v", value, err)
		}
		if len(secret) != EntropyBytes {
			t.Fatalf("secret length = %d, want %d", len(secret), EntropyBytes)
		}
		if _, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, prefix)); err != nil {
			t.Fatalf("token secret is not raw base64url: %v", err)
		}
	}
}

func TestGenerateProducesUniqueLookingTokens(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for range 128 {
		value, err := GenerateDropToken()
		if err != nil {
			t.Fatalf("GenerateDropToken: %v", err)
		}
		if _, ok := seen[value]; ok {
			t.Fatalf("duplicate token generated: %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestHashSecretFormatAndVerification(t *testing.T) {
	plaintext := "api_example"
	hash := HashSecret(plaintext)

	if !ValidSHA256Hash(hash) {
		t.Fatalf("hash %q is not a valid sha256 hash", hash)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash %q missing sha256 prefix", hash)
	}
	if !VerifySecretHash(plaintext, hash) {
		t.Fatal("VerifySecretHash rejected matching plaintext")
	}
	if VerifySecretHash("api_other", hash) {
		t.Fatal("VerifySecretHash accepted wrong plaintext")
	}
	if VerifySecretHash(plaintext, "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA") {
		t.Fatal("VerifySecretHash accepted malformed uppercase hash")
	}
}

func TestEqualHash(t *testing.T) {
	hash := HashSecret("drop_secret")
	if !EqualHash(hash, hash) {
		t.Fatal("EqualHash rejected identical hashes")
	}
	if EqualHash(hash, HashSecret("other")) {
		t.Fatal("EqualHash accepted different hashes")
	}
}
