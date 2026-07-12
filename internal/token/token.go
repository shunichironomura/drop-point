package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	DropPointIDPrefix = "dp_"
	DropTokenPrefix   = "drop_"
	PickupTokenPrefix = "pick_"
	APITokenPrefix    = "api_"

	// entropyBytes gives every generated capability token at least 256 bits of
	// CSPRNG entropy. Public drop point IDs use the same size for simplicity.
	entropyBytes = 32

	hashSchemeSHA256 = "sha256"
)

var encoding = base64.RawURLEncoding

// GenerateDropPointID returns a public drop point identifier with a dp_ prefix.
func GenerateDropPointID() (string, error) {
	return generate(DropPointIDPrefix)
}

// GenerateDropToken returns a sender capability token with a drop_ prefix.
func GenerateDropToken() (string, error) {
	return generate(DropTokenPrefix)
}

// GeneratePickupToken returns a receiver capability token with a pick_ prefix.
func GeneratePickupToken() (string, error) {
	return generate(PickupTokenPrefix)
}

// GenerateAPIToken returns an operator API bearer token with an api_ prefix.
func GenerateAPIToken() (string, error) {
	return generate(APITokenPrefix)
}

// generate returns prefix followed by 32 bytes of base64url-without-padding
// CSPRNG output.
func generate(prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("token prefix must not be empty")
	}
	secret := make([]byte, entropyBytes)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate token entropy: %w", err)
	}
	return prefix + encoding.EncodeToString(secret), nil
}

func decodeSecret(value, prefix string) ([]byte, error) {
	if !strings.HasPrefix(value, prefix) {
		return nil, fmt.Errorf("token must use %q prefix", prefix)
	}
	secret, err := encoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return nil, fmt.Errorf("decode token secret: %w", err)
	}
	return secret, nil
}

// HashSecret returns sha256:<lowercase-hex-sha256> for a plaintext token.
func HashSecret(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hashSchemeSHA256 + ":" + hex.EncodeToString(sum[:])
}

func verifySecretHash(plaintext string, storedHash string) bool {
	if !ValidSHA256Hash(storedHash) {
		return false
	}
	computed := HashSecret(plaintext)
	return subtle.ConstantTimeCompare([]byte(computed), []byte(storedHash)) == 1
}

// EqualHash compares two stored token hashes in constant time when both use the
// supported hash format.
func EqualHash(a, b string) bool {
	if !ValidSHA256Hash(a) || !ValidSHA256Hash(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidSHA256Hash reports whether hash uses sha256:<64 lowercase hex digits>.
func ValidSHA256Hash(hash string) bool {
	if len(hash) != len(hashSchemeSHA256)+1+64 {
		return false
	}
	if !strings.HasPrefix(hash, hashSchemeSHA256+":") {
		return false
	}
	for _, r := range hash[len(hashSchemeSHA256)+1:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
