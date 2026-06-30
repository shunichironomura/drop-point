package cryptoenv

import "time"

type TestVector struct {
	Name                      string `json:"name"`
	RecipientPrivateKey       string `json:"recipient_private_key"`
	RecipientPublicKey        string `json:"recipient_public_key"`
	SenderEphemeralPrivateKey string `json:"sender_ephemeral_private_key"`
	SenderEphemeralPublicKey  string `json:"sender_ephemeral_public_key"`
	MetadataNonce             string `json:"metadata_nonce"`
	PayloadNonce              string `json:"payload_nonce"`
	SharedSecret              string `json:"shared_secret"`
	MetadataKey               string `json:"metadata_key"`
	PayloadKey                string `json:"payload_key"`
	ManifestJSON              string `json:"manifest_json"`
	EnvelopeJSON              string `json:"envelope_json"`
	EncryptedMetadata         string `json:"encrypted_metadata"`
	EncryptedPayload          string `json:"encrypted_payload"`
	PayloadPlaintext          string `json:"payload_plaintext"`
}

func PositiveTestVectors() ([]TestVector, error) {
	recipientPrivate := sequenceBytes(1, 32)
	recipientPublic, err := PublicKeyFromPrivate(recipientPrivate)
	if err != nil {
		return nil, err
	}
	senderPrivate := sequenceBytes(65, 32)
	metadataNonce := sequenceBytes(129, AESGCMNonceBytes)
	payloadNonce := sequenceBytes(161, AESGCMNonceBytes)
	createdAt := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	definitions := []struct {
		name  string
		files []PlainFile
	}{
		{
			name:  "single-file",
			files: []PlainFile{{Name: "scan-01.txt", Type: "text/plain", Data: []byte("hello drop point\n")}},
		},
		{
			name: "multi-file",
			files: []PlainFile{
				{Name: "scan-01.txt", Type: "text/plain", Data: []byte("first file\n")},
				{Name: "scan-02.bin", Type: "application/octet-stream", Data: []byte{0, 1, 2, 3, 4, 5}},
			},
		},
	}
	vectors := make([]TestVector, 0, len(definitions))
	for _, definition := range definitions {
		result, err := EncryptBundle(recipientPublic, definition.files, EncryptOptions{
			SenderPrivateKey: senderPrivate,
			MetadataNonce:    metadataNonce,
			PayloadNonce:     payloadNonce,
			CreatedAt:        createdAt,
		})
		if err != nil {
			return nil, err
		}
		senderPublic, err := PublicKeyFromPrivate(senderPrivate)
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, TestVector{
			Name:                      definition.name,
			RecipientPrivateKey:       EncodeBase64URL(recipientPrivate),
			RecipientPublicKey:        EncodeBase64URL(recipientPublic),
			SenderEphemeralPrivateKey: EncodeBase64URL(senderPrivate),
			SenderEphemeralPublicKey:  EncodeBase64URL(senderPublic),
			MetadataNonce:             EncodeBase64URL(metadataNonce),
			PayloadNonce:              EncodeBase64URL(payloadNonce),
			SharedSecret:              EncodeBase64URL(result.Intermediates.SharedSecret),
			MetadataKey:               EncodeBase64URL(result.Intermediates.MetadataKey),
			PayloadKey:                EncodeBase64URL(result.Intermediates.PayloadKey),
			ManifestJSON:              string(result.ManifestJSON),
			EnvelopeJSON:              string(result.EnvelopeJSON),
			EncryptedMetadata:         EncodeBase64URL(result.Intermediates.EncryptedMetadata),
			EncryptedPayload:          EncodeBase64URL(result.EncryptedPayload),
			PayloadPlaintext:          EncodeBase64URL(result.PayloadPlaintext),
		})
	}
	return vectors, nil
}

func sequenceBytes(start byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = start + byte(i)
	}
	return out
}
