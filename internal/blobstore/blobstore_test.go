package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/shunichironomura/droppoint/internal/config"
	"github.com/shunichironomura/droppoint/internal/droppoint"
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
	gotEnvelope, err := store.ReadEnvelope(context.Background(), result.EnvelopePath)
	if err != nil {
		t.Fatalf("ReadEnvelope: %v", err)
	}
	if !bytes.Equal(gotEnvelope, envelope) {
		t.Fatalf("envelope bytes = %q, want %q", gotEnvelope, envelope)
	}
	payloadReader, err := store.OpenPayload(context.Background(), result.PayloadPath)
	if err != nil {
		t.Fatalf("OpenPayload: %v", err)
	}
	gotPayload, err := io.ReadAll(payloadReader)
	_ = payloadReader.Close()
	if err != nil {
		t.Fatalf("ReadAll payload: %v", err)
	}
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

func TestBlobOperationsHonorCanceledContext(t *testing.T) {
	store := newTestBlobStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.WriteDrop(ctx, "dp_canceled_write", []byte(`{}`), bytes.NewReader([]byte("payload")), 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteDrop err = %v, want context.Canceled", err)
	}
	if _, err := store.ReadEnvelope(ctx, "drop-points/dp_canceled_write/envelope.json"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadEnvelope err = %v, want context.Canceled", err)
	}
	if _, err := store.OpenPayload(ctx, "drop-points/dp_canceled_write/payload.bin"); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenPayload err = %v, want context.Canceled", err)
	}
	if err := store.DeleteDropPoint(ctx, "dp_canceled_write"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteDropPoint err = %v, want context.Canceled", err)
	}
}

func TestWriteDropClassifiesUploaderReadFailure(t *testing.T) {
	store := newTestBlobStore(t)
	_, err := store.WriteDrop(context.Background(), "dp_read_failure", []byte(`{}`), errorReader{}, 10)
	if !errors.Is(err, ErrSourceRead) {
		t.Fatalf("WriteDrop err = %v, want ErrSourceRead", err)
	}
	if got := ClassifyFailure(err); got != FailureClientInput {
		t.Fatalf("ClassifyFailure = %v, want FailureClientInput", got)
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

func TestDeleteDropPointRejectsReservedAndNonDropPointIDs(t *testing.T) {
	store := newTestBlobStore(t)
	for _, id := range []string{".", "..", "other", "dp_", "dp_bad.name", "dp_bad/name", `dp_bad\\name`} {
		t.Run(id, func(t *testing.T) {
			if err := store.DeleteDropPoint(context.Background(), id); err == nil {
				t.Fatal("DeleteDropPoint succeeded, want invalid id error")
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func newTestBlobStore(t *testing.T) *Store {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := config.EnsureDataDir(dataDir); err != nil {
		t.Fatalf("EnsureDataDir: %v", err)
	}
	return New(dataDir)
}
