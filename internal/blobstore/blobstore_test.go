package blobstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shunichironomura/drop-point/internal/config"
	"github.com/shunichironomura/drop-point/internal/droppoint"
)

func TestWriteDropStoresExactBytes(t *testing.T) {
	store := newTestBlobStore(t)
	envelope := []byte(`{"protocol_version":2}`)
	payload := []byte{0, 1, 2, 3, 4}

	result, err := store.WriteDrop(context.Background(), "dp_blob", envelope, bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	if result.EncryptedSize != int64(len(payload)) {
		t.Fatalf("EncryptedSize = %d, want %d", result.EncryptedSize, len(payload))
	}
	gotEnvelope := mustReadBlob(t, store, result.EnvelopePath)
	if !bytes.Equal(gotEnvelope, envelope) {
		t.Fatalf("envelope bytes = %q, want %q", gotEnvelope, envelope)
	}
	gotPayload := mustReadBlob(t, store, result.PayloadPath)
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload bytes = %v, want %v", gotPayload, payload)
	}
}

func TestWriteDropRejectsOversizeWithoutFinalFiles(t *testing.T) {
	store := newTestBlobStore(t)
	err := error(nil)
	_, err = store.WriteDrop(context.Background(), "dp_big", []byte(`{}`), bytes.NewReader([]byte("12345")), 4)
	if !errors.Is(err, droppoint.ErrPayloadTooLarge) {
		t.Fatalf("WriteDrop err = %v, want ErrPayloadTooLarge", err)
	}
	if _, err := os.Stat(filepath.Join(store.DropDir("dp_big"), PayloadFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("payload final stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(store.DropDir("dp_big"), EnvelopeFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("envelope final stat err = %v, want not exist", err)
	}
}

func TestDeleteDropPointIsIdempotent(t *testing.T) {
	store := newTestBlobStore(t)
	if _, err := store.WriteDrop(context.Background(), "dp_delete", []byte(`{}`), bytes.NewReader([]byte("payload")), 10); err != nil {
		t.Fatalf("WriteDrop: %v", err)
	}
	for range 2 {
		if err := store.DeleteDropPoint(context.Background(), "dp_delete"); err != nil {
			t.Fatalf("DeleteDropPoint: %v", err)
		}
	}
	if _, err := os.Stat(store.DropDir("dp_delete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drop dir stat err = %v, want not exist", err)
	}
}

func newTestBlobStore(t *testing.T) *Store {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	return New(dataDir)
}

func mustReadBlob(t *testing.T, store *Store, relative string) []byte {
	t.Helper()
	path, err := store.Path(relative)
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}
