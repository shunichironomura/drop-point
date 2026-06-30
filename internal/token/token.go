// Package token generates and hashes the prefixed identifiers and capability
// tokens DropPoint uses (SPEC §6). It belongs to the functional core: only the
// CSPRNG read reaches outside the process.
//
// Capability tokens (drop, pickup, API) carry at least 256 bits of entropy and
// are encoded as base64url without padding. Raw tokens MUST NOT be stored or
// logged; persist only the Hash of a token (SPEC §6.4, §8, §15).
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Type prefixes for identifiers and capability tokens (SPEC §6.4). The full
// value is "<prefix>_<base64url-no-pad>".
const (
	PrefixDropPoint   = "dp"
	PrefixDropToken   = "drop"
	PrefixPickupToken = "pick"
	PrefixAPIToken    = "api"
)

const (
	// idEntropyBytes sizes public identifiers (drop point IDs). 128 bits makes
	// IDs unguessable; IDs are not secrets, so they need not reach 256 bits.
	idEntropyBytes = 16
	// tokenEntropyBytes sizes capability-token secrets at 256 bits (SPEC §6.4).
	tokenEntropyBytes = 32
)

// hashPrefix labels stored token hashes and matches the API-token hash format in
// configuration, so the same Hash output can populate config or a database row
// (SPEC §9).
const hashPrefix = "sha256:"

// enc is base64url without padding, the encoding SPEC §10.1 mandates for binary
// values carried in JSON or URLs.
var enc = base64.RawURLEncoding

// NewDropPointID returns a fresh public drop point identifier, "dp_<base64url>".
func NewDropPointID() (string, error) { return generate(PrefixDropPoint, idEntropyBytes) }

// NewDropToken returns a fresh sender capability token, "drop_<base64url>", with
// at least 256 bits of entropy.
func NewDropToken() (string, error) { return generate(PrefixDropToken, tokenEntropyBytes) }

// NewPickupToken returns a fresh receiver capability token, "pick_<base64url>",
// with at least 256 bits of entropy.
func NewPickupToken() (string, error) { return generate(PrefixPickupToken, tokenEntropyBytes) }

// NewAPIToken returns a fresh API token, "api_<base64url>", with at least 256
// bits of entropy. It supports operator token-provisioning tooling; the relay
// itself stores only the configured hash (SPEC §9).
func NewAPIToken() (string, error) { return generate(PrefixAPIToken, tokenEntropyBytes) }

// generate reads n random bytes from the CSPRNG and returns
// "<prefix>_<base64url-no-pad>". An error means the CSPRNG failed and the caller
// MUST NOT fall back to a weaker source.
func generate(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token: read entropy: %w", err)
	}
	return prefix + "_" + enc.EncodeToString(b), nil
}

// Hash returns the at-rest hash of a raw token: "sha256:<lowercase-hex>". Only
// this value is stored; raw tokens are never persisted (SPEC §6.4, §8).
func Hash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hashPrefix + hex.EncodeToString(sum[:])
}

// EqualHash reports whether two stored token hashes are equal, comparing in
// constant time so a near-miss is indistinguishable from a far-miss (SPEC §6.4).
// Hashes are fixed-length, so the length-dependent fast path in
// subtle.ConstantTimeCompare does not leak useful information here.
func EqualHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
