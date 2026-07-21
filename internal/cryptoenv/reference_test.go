package cryptoenv

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReferenceEncryptDecryptRoundTrip(t *testing.T) {
	recipientPrivate := sequenceBytes(1, 32)
	recipientPublic, err := PublicKeyFromPrivate(recipientPrivate)
	if err != nil {
		t.Fatalf("PublicKeyFromPrivate: %v", err)
	}
	files := []PlainFile{
		{Name: "scan-01.txt", Type: "text/plain", Data: []byte("hello\n")},
		{Name: "scan-02.bin", Type: "application/octet-stream", Data: []byte{0, 1, 2, 3}},
	}

	result, err := EncryptBundle(recipientPublic, files, EncryptOptions{
		SenderPrivateKey: sequenceBytes(65, 32),
		MetadataNonce:    sequenceBytes(129, AESGCMNonceBytes),
		PayloadNonce:     sequenceBytes(161, AESGCMNonceBytes),
		CreatedAt:        time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("EncryptBundle: %v", err)
	}
	parsedEnvelope, err := ParseEnvelopeJSON(result.EnvelopeJSON)
	if err != nil {
		t.Fatalf("ParseEnvelopeJSON: %v", err)
	}
	recovered, intermediates, err := DecryptBundle(recipientPrivate, parsedEnvelope, result.EncryptedPayload)
	if err != nil {
		t.Fatalf("DecryptBundle: %v", err)
	}
	if len(recovered) != len(files) {
		t.Fatalf("len(recovered) = %d, want %d", len(recovered), len(files))
	}
	for i := range files {
		if recovered[i].SafeName != files[i].Name || recovered[i].SafeType == "" || !bytes.Equal(recovered[i].Data, files[i].Data) {
			t.Fatalf("recovered[%d] = %+v, want %+v", i, recovered[i], files[i])
		}
	}
	if !bytes.Equal(intermediates.SharedSecret, result.Intermediates.SharedSecret) {
		t.Fatalf("shared secret mismatch")
	}
}

func TestPositiveTestVectorsRoundTrip(t *testing.T) {
	vectors, err := PositiveTestVectors()
	if err != nil {
		t.Fatalf("PositiveTestVectors: %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("len(vectors) = %d, want 2", len(vectors))
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			recipientPrivate, err := DecodeBase64URL(vector.RecipientPrivateKey)
			if err != nil {
				t.Fatalf("decode recipient private: %v", err)
			}
			var envelope Envelope
			if err := json.Unmarshal([]byte(vector.EnvelopeJSON), &envelope); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			encryptedPayload, err := DecodeBase64URL(vector.EncryptedPayload)
			if err != nil {
				t.Fatalf("decode encrypted payload: %v", err)
			}
			files, _, err := DecryptBundle(recipientPrivate, envelope, encryptedPayload)
			if err != nil {
				t.Fatalf("DecryptBundle: %v", err)
			}
			if len(files) == 0 {
				t.Fatal("vector did not recover files")
			}
		})
	}
}

func TestNegativeVectorsAreRejected(t *testing.T) {
	recipientPrivate := sequenceBytes(1, 32)
	recipientPublic, err := PublicKeyFromPrivate(recipientPrivate)
	if err != nil {
		t.Fatalf("PublicKeyFromPrivate: %v", err)
	}
	result, err := EncryptBundle(recipientPublic, []PlainFile{{Name: "scan.txt", Type: "text/plain", Data: []byte("payload")}}, EncryptOptions{
		SenderPrivateKey: sequenceBytes(65, 32),
		MetadataNonce:    sequenceBytes(129, AESGCMNonceBytes),
		PayloadNonce:     sequenceBytes(161, AESGCMNonceBytes),
		CreatedAt:        time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("EncryptBundle: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(Envelope, []byte) (Envelope, []byte)
		wantErr string
	}{
		{name: "tampered payload ciphertext tag", mutate: func(e Envelope, p []byte) (Envelope, []byte) { p[0] ^= 0x80; return e, p }, wantErr: "decrypt payload"},
		{name: "tampered metadata ciphertext tag", mutate: func(e Envelope, p []byte) (Envelope, []byte) {
			metadata, _ := DecodeBase64URL(e.EncryptedMetadata)
			metadata[0] ^= 0x80
			e.EncryptedMetadata = EncodeBase64URL(metadata)
			return e, p
		}, wantErr: "decrypt metadata"},
		{name: "tampered nonce", mutate: func(e Envelope, p []byte) (Envelope, []byte) {
			nonce, _ := DecodeBase64URL(e.PayloadNonce)
			nonce[0] ^= 0x80
			e.PayloadNonce = EncodeBase64URL(nonce)
			return e, p
		}, wantErr: "decrypt payload"},
		{name: "tampered sender ephemeral public key", mutate: func(e Envelope, p []byte) (Envelope, []byte) {
			publicKey, _ := DecodeBase64URL(e.SenderEphemeralPublicKey)
			publicKey[0] ^= 0x80
			e.SenderEphemeralPublicKey = EncodeBase64URL(publicKey)
			return e, p
		}, wantErr: "decrypt metadata"},
		{name: "protocol version downgrade", mutate: func(e Envelope, p []byte) (Envelope, []byte) { e.ProtocolVersion = 1; return e, p }, wantErr: "envelope invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope, payload := tt.mutate(result.Envelope, append([]byte{}, result.EncryptedPayload...))
			_, _, err := DecryptBundle(recipientPrivate, envelope, payload)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
			}
		})
	}

	wrongRecipientPrivate := sequenceBytes(33, 32)
	_, _, err = DecryptBundle(wrongRecipientPrivate, result.Envelope, result.EncryptedPayload)
	if err == nil || !strings.Contains(err.Error(), "decrypt metadata") {
		t.Fatalf("wrong recipient err = %v", err)
	}
	if _, err := SharedSecret(recipientPrivate, make([]byte, 32)); err == nil {
		t.Fatal("low-order/all-zero X25519 public key was accepted")
	}
}

func TestFilenamePolicyFixtures(t *testing.T) {
	data, err := os.ReadFile("../../testdata/filename-policy.json")
	if err != nil {
		t.Fatalf("ReadFile filename policy: %v", err)
	}
	var fixture struct {
		MaxFilenameUTF8Bytes int `json:"max_filename_utf8_bytes"`
		MaxManifestFiles     int `json:"max_manifest_files"`
		Canonicalization     []struct {
			Input  []string `json:"input"`
			Output []string `json:"output"`
		} `json:"canonicalization_bundles"`
		Validation []struct {
			Name  string `json:"name"`
			Valid bool   `json:"valid"`
		} `json:"validation"`
		Collisions []struct {
			Names    []string `json:"names"`
			Collides bool     `json:"collides"`
		} `json:"collisions"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("Unmarshal filename policy: %v", err)
	}
	if fixture.MaxFilenameUTF8Bytes != MaxFilenameUTF8Bytes || fixture.MaxManifestFiles != MaxManifestFiles {
		t.Fatalf("fixture limits = %d/%d, code limits = %d/%d", fixture.MaxFilenameUTF8Bytes, fixture.MaxManifestFiles, MaxFilenameUTF8Bytes, MaxManifestFiles)
	}
	for i, bundle := range fixture.Canonicalization {
		got, err := CanonicalizeFilenames(bundle.Input)
		if err != nil {
			t.Fatalf("canonicalization bundle %d: %v", i, err)
		}
		if !slices.Equal(got, bundle.Output) {
			t.Fatalf("canonicalization bundle %d = %#v, want %#v", i, got, bundle.Output)
		}
		for _, name := range got {
			if _, err := SanitizeFilename(name); err != nil {
				t.Fatalf("canonical output %q is invalid: %v", name, err)
			}
		}
	}
	for _, entry := range fixture.Validation {
		_, err := SanitizeFilename(entry.Name)
		if gotValid := err == nil; gotValid != entry.Valid {
			t.Fatalf("SanitizeFilename(%q) valid = %t, want %t (err=%v)", entry.Name, gotValid, entry.Valid, err)
		}
	}
	for _, entry := range fixture.Collisions {
		if len(entry.Names) != 2 {
			t.Fatalf("collision fixture names = %#v, want two", entry.Names)
		}
		got := filenameCollisionKey(entry.Names[0]) == filenameCollisionKey(entry.Names[1])
		if got != entry.Collides {
			t.Fatalf("collision(%q, %q) = %t, want %t", entry.Names[0], entry.Names[1], got, entry.Collides)
		}
	}
}

func TestManifestEnforcesEntryAndFilenameBounds(t *testing.T) {
	tooMany := make([]PlainFile, MaxManifestFiles+1)
	if _, _, err := BuildManifest(tooMany, time.Now()); err == nil {
		t.Fatal("BuildManifest accepted too many files")
	}
	longNames := []string{strings.Repeat("é", MaxFilenameUTF8Bytes), strings.Repeat("é", MaxFilenameUTF8Bytes)}
	canonical, err := CanonicalizeFilenames(longNames)
	if err != nil {
		t.Fatalf("CanonicalizeFilenames: %v", err)
	}
	for _, name := range canonical {
		if len(name) > MaxFilenameUTF8Bytes {
			t.Fatalf("canonical filename length = %d, want <= %d", len(name), MaxFilenameUTF8Bytes)
		}
	}
}

func TestManifestSizeOverflowIsRejectedWithoutPanic(t *testing.T) {
	manifest := Manifest{
		ProtocolVersion: ProtocolVersion,
		CreatedAt:       time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Files: []ManifestFile{
			{Name: "one.bin", Type: "application/octet-stream", Size: math.MaxInt64},
			{Name: "two.bin", Type: "application/octet-stream", Size: math.MaxInt64},
			{Name: "three.bin", Type: "application/octet-stream", Size: 2},
		},
	}
	if err := ValidateManifest(manifest, nil); err == nil {
		t.Fatal("ValidateManifest accepted overflowing size sum")
	}
	if _, err := SplitPayload(manifest, nil); err == nil {
		t.Fatal("SplitPayload accepted overflowing size sum")
	}
}

func FuzzSplitPayloadNeverPanics(f *testing.F) {
	f.Add([]byte(`{"protocol_version":2,"files":[{"name":"safe.txt","type":"text/plain","size":0}],"created_at":"2026-06-30T12:00:00Z"}`), []byte{})
	f.Add([]byte(`{"protocol_version":2,"files":[{"name":"one","type":"application/octet-stream","size":9223372036854775807},{"name":"two","type":"application/octet-stream","size":9223372036854775807},{"name":"three","type":"application/octet-stream","size":2}],"created_at":"2026-06-30T12:00:00Z"}`), []byte{})
	f.Fuzz(func(t *testing.T, manifestJSON []byte, payload []byte) {
		var manifest Manifest
		if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
			return
		}
		_, _ = SplitPayload(manifest, payload)
	})
}

func TestManifestValidationAndSanitization(t *testing.T) {
	createdAt := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)
	manifest, payload, err := BuildManifest([]PlainFile{{Name: "safe.txt", Type: "TEXT/PLAIN", Data: []byte("abc")}}, createdAt)
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if err := ValidateManifest(manifest, payload); err != nil {
		t.Fatalf("ValidateManifest: %v", err)
	}
	files, err := SplitPayload(manifest, payload)
	if err != nil {
		t.Fatalf("SplitPayload: %v", err)
	}
	if files[0].SafeName != "safe.txt" || files[0].SafeType != "text/plain" {
		t.Fatalf("safe fields = %+v", files[0])
	}

	tests := map[string]func(Manifest, []byte) (Manifest, []byte){
		"manifest size-sum mismatch": func(m Manifest, p []byte) (Manifest, []byte) { m.Files[0].Size++; return m, p },
		"hostile filename":           func(m Manifest, p []byte) (Manifest, []byte) { m.Files[0].Name = "../evil.txt"; return m, p },
		"hostile MIME type":          func(m Manifest, p []byte) (Manifest, []byte) { m.Files[0].Type = "text/plain\nX-Evil: 1"; return m, p },
		"duplicate filenames": func(m Manifest, p []byte) (Manifest, []byte) {
			m.Files = append(m.Files, ManifestFile{Name: "SAFE.txt", Type: "text/plain", Size: 0})
			return m, p
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			mutatedManifest, mutatedPayload := mutate(manifest, payload)
			if err := ValidateManifest(mutatedManifest, mutatedPayload); err == nil {
				t.Fatal("ValidateManifest succeeded, want error")
			}
		})
	}
}
