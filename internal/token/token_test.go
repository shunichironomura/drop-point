package token

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// splitToken separates a "<prefix>_<body>" value. It fails the test if the shape
// is wrong so every generator is held to the SPEC §6.4 format.
func splitToken(t *testing.T, tok string) (prefix, body string) {
	t.Helper()
	i := strings.IndexByte(tok, '_')
	if i <= 0 || i == len(tok)-1 {
		t.Fatalf("token %q is not of the form <prefix>_<body>", tok)
	}
	return tok[:i], tok[i+1:]
}

func TestNewDropPointIDFormat(t *testing.T) {
	id, err := NewDropPointID()
	if err != nil {
		t.Fatalf("NewDropPointID: %v", err)
	}
	prefix, body := splitToken(t, id)
	if prefix != PrefixDropPoint {
		t.Errorf("prefix = %q, want %q", prefix, PrefixDropPoint)
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("body is not base64url-no-pad: %v", err)
	}
	if len(raw) != idEntropyBytes {
		t.Errorf("id entropy = %d bytes, want %d", len(raw), idEntropyBytes)
	}
}

func TestCapabilityTokensHave256BitEntropy(t *testing.T) {
	cases := []struct {
		name   string
		gen    func() (string, error)
		prefix string
	}{
		{"drop", NewDropToken, PrefixDropToken},
		{"pickup", NewPickupToken, PrefixPickupToken},
		{"api", NewAPIToken, PrefixAPIToken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := tc.gen()
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			prefix, body := splitToken(t, tok)
			if prefix != tc.prefix {
				t.Errorf("prefix = %q, want %q", prefix, tc.prefix)
			}
			raw, err := base64.RawURLEncoding.DecodeString(body)
			if err != nil {
				t.Fatalf("body is not base64url-no-pad: %v", err)
			}
			if len(raw) < 32 {
				t.Errorf("capability token entropy = %d bytes (%d bits), want >= 256 bits",
					len(raw), len(raw)*8)
			}
		})
	}
}

func TestTokensUseBase64URLWithoutPadding(t *testing.T) {
	gens := []func() (string, error){NewDropPointID, NewDropToken, NewPickupToken, NewAPIToken}
	for _, gen := range gens {
		tok, err := gen()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		_, body := splitToken(t, tok)
		if strings.ContainsAny(body, "+/=") {
			t.Errorf("token body %q contains non-base64url or padding characters", body)
		}
		// Standard-encoding (with padding) decode must fail for an unpadded body of
		// these lengths, confirming the encoding is the raw URL variant.
		if _, err := base64.RawURLEncoding.DecodeString(body); err != nil {
			t.Errorf("token body %q does not decode as base64url-no-pad: %v", body, err)
		}
	}
}

func TestTokensAreUnique(t *testing.T) {
	const n = 1000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		tok, err := NewDropToken()
		if err != nil {
			t.Fatalf("NewDropToken: %v", err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token generated within %d draws: %q", n, tok)
		}
		seen[tok] = struct{}{}
	}
}

var storedHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func TestHashFormat(t *testing.T) {
	h := Hash("drop_example-secret")
	if !storedHashPattern.MatchString(h) {
		t.Errorf("Hash output %q does not match sha256:<64 lowercase hex>", h)
	}
	// Deterministic.
	if h2 := Hash("drop_example-secret"); h2 != h {
		t.Errorf("Hash is not deterministic: %q != %q", h, h2)
	}
	// Distinct inputs yield distinct hashes.
	if Hash("a") == Hash("b") {
		t.Error("Hash collided for distinct inputs")
	}
}

func TestHashDoesNotRevealRawToken(t *testing.T) {
	raw := "pick_super-secret-token-value"
	if strings.Contains(Hash(raw), raw) {
		t.Error("Hash output contains the raw token; raw tokens must not be stored")
	}
}

func TestEqualHash(t *testing.T) {
	a := Hash("drop_token-a")
	b := Hash("drop_token-b")

	if !EqualHash(a, a) {
		t.Error("EqualHash(a, a) = false, want true")
	}
	if EqualHash(a, b) {
		t.Error("EqualHash(a, b) = true, want false")
	}
	// Differing lengths must compare unequal without panicking.
	if EqualHash(a, a+"extra") {
		t.Error("EqualHash with different lengths = true, want false")
	}
	if EqualHash("", a) {
		t.Error("EqualHash(empty, a) = true, want false")
	}
}
