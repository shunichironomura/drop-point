package cryptoenv

import (
	"errors"
	"strings"
	"testing"

	"github.com/shunichironomura/droppoint/internal/droppoint"
)

func TestValidateEnvelopeJSONAcceptsValidShape(t *testing.T) {
	envelopeJSON := validEnvelopeJSON()
	envelope, err := ValidateEnvelopeJSON([]byte(envelopeJSON))
	if err != nil {
		t.Fatalf("ValidateEnvelopeJSON: %v", err)
	}
	if envelope.ProtocolVersion != ProtocolVersion || envelope.KeyAgreement != KeyAgreement {
		t.Fatalf("envelope mismatch: %+v", envelope)
	}
}

func TestValidateEnvelopeJSONRejectsInvalidShape(t *testing.T) {
	tests := map[string]string{
		"version":           strings.Replace(validEnvelopeJSON(), `"protocol_version":2`, `"protocol_version":1`, 1),
		"algorithm":         strings.Replace(validEnvelopeJSON(), KeyAgreement, "rsa-oaep", 1),
		"public key length": strings.Replace(validEnvelopeJSON(), EncodeBase64URL(make([]byte, 32)), EncodeBase64URL(make([]byte, 31)), 1),
		"nonce length":      strings.Replace(validEnvelopeJSON(), EncodeBase64URL(make([]byte, 12)), EncodeBase64URL(make([]byte, 11)), 1),
		"padding":           strings.Replace(validEnvelopeJSON(), EncodeBase64URL(make([]byte, 16)), EncodeBase64URL(make([]byte, 16))+"=", 1),
		"unknown field":     strings.TrimSuffix(validEnvelopeJSON(), "}") + `,"extra":true}`,
		"duplicate field":   strings.Replace(validEnvelopeJSON(), `"protocol_version":2`, `"protocol_version":2,"protocol_version":2`, 1),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ValidateEnvelopeJSON([]byte(body))
			if !errors.Is(err, droppoint.ErrEnvelopeInvalid) {
				t.Fatalf("err = %v, want ErrEnvelopeInvalid", err)
			}
		})
	}
}

func validEnvelopeJSON() string {
	publicKey := make([]byte, 32)
	metadataNonce := make([]byte, 12)
	payloadNonce := make([]byte, 12)
	encryptedMetadata := make([]byte, 16)
	return `{"protocol_version":2,"key_agreement":"` + KeyAgreement + `","sender_ephemeral_public_key":"` + EncodeBase64URL(publicKey) + `","metadata_nonce":"` + EncodeBase64URL(metadataNonce) + `","payload_nonce":"` + EncodeBase64URL(payloadNonce) + `","encrypted_metadata":"` + EncodeBase64URL(encryptedMetadata) + `"}`
}
