package cryptoenv

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/shunichironomura/droppoint/internal/droppoint"
)

const (
	ProtocolVersion = 2
	KeyAgreement    = "x25519-hkdf-sha256-aesgcm-raw32"

	X25519PublicKeyBytes = 32
	AESGCMNonceBytes     = 12
	AESGCMTagBytes       = 16
)

// Envelope is the protocol_version=2 relay-visible encrypted metadata envelope.
type Envelope struct {
	ProtocolVersion          int    `json:"protocol_version"`
	KeyAgreement             string `json:"key_agreement"`
	SenderEphemeralPublicKey string `json:"sender_ephemeral_public_key"`
	MetadataNonce            string `json:"metadata_nonce"`
	PayloadNonce             string `json:"payload_nonce"`
	EncryptedMetadata        string `json:"encrypted_metadata"`
}

// ValidateEnvelopeJSON validates the relay-visible envelope shape without
// decrypting metadata or payload bytes.
func ValidateEnvelopeJSON(data []byte) (*Envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, invalidEnvelope("decode envelope JSON: %w", err)
	}
	var trailing any
	switch err := decoder.Decode(&trailing); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return nil, invalidEnvelope("envelope JSON must contain one value")
	default:
		return nil, invalidEnvelope("decode envelope JSON: %w", err)
	}
	if envelope.ProtocolVersion != ProtocolVersion {
		return nil, invalidEnvelope("protocol_version must be %d", ProtocolVersion)
	}
	if envelope.KeyAgreement != KeyAgreement {
		return nil, invalidEnvelope("key_agreement must be %q", KeyAgreement)
	}
	if _, err := decodeBase64URLField("sender_ephemeral_public_key", envelope.SenderEphemeralPublicKey, X25519PublicKeyBytes, X25519PublicKeyBytes); err != nil {
		return nil, err
	}
	if _, err := decodeBase64URLField("metadata_nonce", envelope.MetadataNonce, AESGCMNonceBytes, AESGCMNonceBytes); err != nil {
		return nil, err
	}
	if _, err := decodeBase64URLField("payload_nonce", envelope.PayloadNonce, AESGCMNonceBytes, AESGCMNonceBytes); err != nil {
		return nil, err
	}
	if _, err := decodeBase64URLField("encrypted_metadata", envelope.EncryptedMetadata, AESGCMTagBytes, 0); err != nil {
		return nil, err
	}
	return &envelope, nil
}

// DecodeBase64URL decodes base64url-without-padding protocol fields.
func DecodeBase64URL(value string) ([]byte, error) {
	if value == "" || strings.Contains(value, "=") {
		return nil, fmt.Errorf("base64url value must be non-empty and unpadded")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

// EncodeBase64URL encodes protocol bytes as base64url without padding.
func EncodeBase64URL(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeBase64URLField(name string, value string, minLen int, exactLen int) ([]byte, error) {
	decoded, err := DecodeBase64URL(value)
	if err != nil {
		return nil, invalidEnvelope("%s must be base64url without padding: %w", name, err)
	}
	if exactLen > 0 && len(decoded) != exactLen {
		return nil, invalidEnvelope("%s decoded length = %d, want %d", name, len(decoded), exactLen)
	}
	if minLen > 0 && len(decoded) < minLen {
		return nil, invalidEnvelope("%s decoded length = %d, want at least %d", name, len(decoded), minLen)
	}
	return decoded, nil
}

func invalidEnvelope(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{droppoint.ErrEnvelopeInvalid}, args...)...)
}
