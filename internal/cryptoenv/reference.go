package cryptoenv

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

const (
	InfoMetadata = "DropPoint/protocol/v2 key=metadata"
	InfoPayload  = "DropPoint/protocol/v2 key=payload"
	aadMetadata  = "\x02metadata"
	aadPayload   = "\x02payload"
)

type KeyPair struct {
	PrivateKey []byte
	PublicKey  []byte
}

type PlainFile struct {
	Name string
	Type string
	Data []byte
}

type RecoveredFile struct {
	Name     string
	SafeName string
	Type     string
	SafeType string
	Data     []byte
}

type EncryptOptions struct {
	SenderPrivateKey []byte
	MetadataNonce    []byte
	PayloadNonce     []byte
	CreatedAt        time.Time
	Random           io.Reader
}

type EncryptResult struct {
	Envelope         Envelope
	EnvelopeJSON     []byte
	EncryptedPayload []byte
	ManifestJSON     []byte
	PayloadPlaintext []byte
	Intermediates    Intermediates
}

type Intermediates struct {
	SharedSecret      []byte
	MetadataKey       []byte
	PayloadKey        []byte
	EncryptedMetadata []byte
}

func GenerateX25519KeyPair(reader io.Reader) (KeyPair, error) {
	if reader == nil {
		reader = rand.Reader
	}
	private, err := ecdh.X25519().GenerateKey(reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate X25519 key pair: %w", err)
	}
	return KeyPair{PrivateKey: private.Bytes(), PublicKey: private.PublicKey().Bytes()}, nil
}

func PublicKeyFromPrivate(privateRaw []byte) ([]byte, error) {
	private, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, fmt.Errorf("parse X25519 private key: %w", err)
	}
	return private.PublicKey().Bytes(), nil
}

func SharedSecret(privateRaw []byte, publicRaw []byte) ([]byte, error) {
	private, err := ecdh.X25519().NewPrivateKey(privateRaw)
	if err != nil {
		return nil, fmt.Errorf("parse X25519 private key: %w", err)
	}
	public, err := ecdh.X25519().NewPublicKey(publicRaw)
	if err != nil {
		return nil, fmt.Errorf("parse X25519 public key: %w", err)
	}
	shared, err := private.ECDH(public)
	if err != nil {
		return nil, fmt.Errorf("compute X25519 shared secret: %w", err)
	}
	if bytes.Equal(shared, make([]byte, X25519PublicKeyBytes)) {
		return nil, fmt.Errorf("compute X25519 shared secret: all-zero shared secret")
	}
	return shared, nil
}

func DeriveKeys(sharedSecret []byte, senderEphemeralPublicKey []byte, recipientPublicKey []byte) ([]byte, []byte, error) {
	if len(sharedSecret) != X25519PublicKeyBytes {
		return nil, nil, fmt.Errorf("shared secret length = %d, want %d", len(sharedSecret), X25519PublicKeyBytes)
	}
	if len(senderEphemeralPublicKey) != X25519PublicKeyBytes || len(recipientPublicKey) != X25519PublicKeyBytes {
		return nil, nil, fmt.Errorf("HKDF salt public keys must both be 32 bytes")
	}
	salt := append(append([]byte{}, senderEphemeralPublicKey...), recipientPublicKey...)
	metadataKey, err := hkdf.Key(sha256.New, sharedSecret, salt, InfoMetadata, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("derive metadata key: %w", err)
	}
	payloadKey, err := hkdf.Key(sha256.New, sharedSecret, salt, InfoPayload, 32)
	if err != nil {
		return nil, nil, fmt.Errorf("derive payload key: %w", err)
	}
	return metadataKey, payloadKey, nil
}

func EncryptBundle(recipientPublicKey []byte, files []PlainFile, opts EncryptOptions) (EncryptResult, error) {
	if len(recipientPublicKey) != X25519PublicKeyBytes {
		return EncryptResult{}, fmt.Errorf("recipient public key length = %d, want %d", len(recipientPublicKey), X25519PublicKeyBytes)
	}
	reader := opts.Random
	if reader == nil {
		reader = rand.Reader
	}
	senderPrivate := opts.SenderPrivateKey
	var senderPublic []byte
	if len(senderPrivate) == 0 {
		keyPair, err := GenerateX25519KeyPair(reader)
		if err != nil {
			return EncryptResult{}, err
		}
		senderPrivate = keyPair.PrivateKey
		senderPublic = keyPair.PublicKey
	} else {
		var err error
		senderPublic, err = PublicKeyFromPrivate(senderPrivate)
		if err != nil {
			return EncryptResult{}, err
		}
	}
	metadataNonce, err := nonceOrRandom(opts.MetadataNonce, reader)
	if err != nil {
		return EncryptResult{}, err
	}
	payloadNonce, err := nonceOrRandom(opts.PayloadNonce, reader)
	if err != nil {
		return EncryptResult{}, err
	}
	createdAt := opts.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	manifest, payloadPlaintext, err := BuildManifest(files, createdAt)
	if err != nil {
		return EncryptResult{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return EncryptResult{}, fmt.Errorf("marshal manifest: %w", err)
	}
	shared, err := SharedSecret(senderPrivate, recipientPublicKey)
	if err != nil {
		return EncryptResult{}, err
	}
	metadataKey, payloadKey, err := DeriveKeys(shared, senderPublic, recipientPublicKey)
	if err != nil {
		return EncryptResult{}, err
	}
	encryptedMetadata, err := encryptAESGCM(metadataKey, metadataNonce, []byte(aadMetadata), manifestJSON)
	if err != nil {
		return EncryptResult{}, err
	}
	encryptedPayload, err := encryptAESGCM(payloadKey, payloadNonce, []byte(aadPayload), payloadPlaintext)
	if err != nil {
		return EncryptResult{}, err
	}
	envelope := Envelope{
		ProtocolVersion:          ProtocolVersion,
		KeyAgreement:             KeyAgreement,
		SenderEphemeralPublicKey: EncodeBase64URL(senderPublic),
		MetadataNonce:            EncodeBase64URL(metadataNonce),
		PayloadNonce:             EncodeBase64URL(payloadNonce),
		EncryptedMetadata:        EncodeBase64URL(encryptedMetadata),
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return EncryptResult{}, fmt.Errorf("marshal envelope: %w", err)
	}
	return EncryptResult{
		Envelope:         envelope,
		EnvelopeJSON:     envelopeJSON,
		EncryptedPayload: encryptedPayload,
		ManifestJSON:     manifestJSON,
		PayloadPlaintext: payloadPlaintext,
		Intermediates: Intermediates{
			SharedSecret:      shared,
			MetadataKey:       metadataKey,
			PayloadKey:        payloadKey,
			EncryptedMetadata: encryptedMetadata,
		},
	}, nil
}

func DecryptBundle(recipientPrivateKey []byte, envelope Envelope, encryptedPayload []byte) ([]RecoveredFile, Intermediates, error) {
	recipientPublicKey, err := PublicKeyFromPrivate(recipientPrivateKey)
	if err != nil {
		return nil, Intermediates{}, err
	}
	senderPublic, metadataNonce, payloadNonce, encryptedMetadata, err := decodeEnvelopeFields(envelope)
	if err != nil {
		return nil, Intermediates{}, err
	}
	shared, err := SharedSecret(recipientPrivateKey, senderPublic)
	if err != nil {
		return nil, Intermediates{}, err
	}
	metadataKey, payloadKey, err := DeriveKeys(shared, senderPublic, recipientPublicKey)
	if err != nil {
		return nil, Intermediates{}, err
	}
	manifestJSON, err := decryptAESGCM(metadataKey, metadataNonce, []byte(aadMetadata), encryptedMetadata)
	if err != nil {
		return nil, Intermediates{}, fmt.Errorf("decrypt metadata: %w", err)
	}
	payloadPlaintext, err := decryptAESGCM(payloadKey, payloadNonce, []byte(aadPayload), encryptedPayload)
	if err != nil {
		return nil, Intermediates{}, fmt.Errorf("decrypt payload: %w", err)
	}
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return nil, Intermediates{}, err
	}
	files, err := SplitPayload(manifest, payloadPlaintext)
	if err != nil {
		return nil, Intermediates{}, err
	}
	return files, Intermediates{SharedSecret: shared, MetadataKey: metadataKey, PayloadKey: payloadKey, EncryptedMetadata: encryptedMetadata}, nil
}

func ParseEnvelopeJSON(data []byte) (Envelope, error) {
	envelope, err := ValidateEnvelopeJSON(data)
	if err != nil {
		return Envelope{}, err
	}
	return *envelope, nil
}

func decodeEnvelopeFields(envelope Envelope) ([]byte, []byte, []byte, []byte, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if _, err := ValidateEnvelopeJSON(encoded); err != nil {
		return nil, nil, nil, nil, err
	}
	senderPublic, err := DecodeBase64URL(envelope.SenderEphemeralPublicKey)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	metadataNonce, err := DecodeBase64URL(envelope.MetadataNonce)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	payloadNonce, err := DecodeBase64URL(envelope.PayloadNonce)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	encryptedMetadata, err := DecodeBase64URL(envelope.EncryptedMetadata)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return senderPublic, metadataNonce, payloadNonce, encryptedMetadata, nil
}

func nonceOrRandom(value []byte, reader io.Reader) ([]byte, error) {
	if len(value) != 0 {
		if len(value) != AESGCMNonceBytes {
			return nil, fmt.Errorf("nonce length = %d, want %d", len(value), AESGCMNonceBytes)
		}
		return append([]byte{}, value...), nil
	}
	nonce := make([]byte, AESGCMNonceBytes)
	if _, err := io.ReadFull(reader, nonce); err != nil {
		return nil, fmt.Errorf("generate AES-GCM nonce: %w", err)
	}
	return nonce, nil
}

func encryptAESGCM(key []byte, nonce []byte, aad []byte, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plaintext, aad), nil
}

func decryptAESGCM(key []byte, nonce []byte, aad []byte, ciphertext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("AES-256 key length = %d, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
