package blobstore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/shunichironomura/drop-point/internal/droppoint"
)

const (
	DropPointsDirName = "drop-points"
	PayloadFileName   = "payload.bin"
	EnvelopeFileName  = "envelope.json"
	fileMode          = 0o600
	dirMode           = 0o700
)

// Store writes encrypted payload and envelope blobs under dataDir/drop-points.
type Store struct {
	dataDir string
}

// New returns a filesystem blob store rooted at dataDir.
func New(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

// WriteDrop atomically stores envelope.json and payload.bin for a drop point.
func (s *Store) WriteDrop(ctx context.Context, id string, envelope []byte, payload io.Reader, maxBytes int64) (droppoint.CommitDropResult, error) {
	if err := validateID(id); err != nil {
		return droppoint.CommitDropResult{}, err
	}
	if maxBytes <= 0 {
		return droppoint.CommitDropResult{}, fmt.Errorf("max bytes must be positive")
	}
	if len(envelope) == 0 {
		return droppoint.CommitDropResult{}, droppoint.ErrEnvelopeInvalid
	}
	dir := s.dropDir(id)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("create drop blob directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("set drop blob directory permissions %q: %w", dir, err)
	}

	token, err := tempToken()
	if err != nil {
		return droppoint.CommitDropResult{}, err
	}
	envelopeTemp := filepath.Join(dir, "."+EnvelopeFileName+"."+token+".tmp")
	payloadTemp := filepath.Join(dir, "."+PayloadFileName+"."+token+".tmp")
	envelopeFinal := filepath.Join(dir, EnvelopeFileName)
	payloadFinal := filepath.Join(dir, PayloadFileName)
	cleanup := func() {
		_ = os.Remove(envelopeTemp)
		_ = os.Remove(payloadTemp)
	}
	defer cleanup()

	if err := writeFileAtomicPart(envelopeTemp, envelope); err != nil {
		return droppoint.CommitDropResult{}, err
	}
	size, err := writeStreamAtomicPart(payloadTemp, payload, maxBytes)
	if err != nil {
		return droppoint.CommitDropResult{}, err
	}
	if err := os.Rename(payloadTemp, payloadFinal); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("install payload blob: %w", err)
	}
	if err := os.Rename(envelopeTemp, envelopeFinal); err != nil {
		_ = os.Remove(payloadFinal)
		return droppoint.CommitDropResult{}, fmt.Errorf("install envelope blob: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return droppoint.CommitDropResult{}, err
	}

	return droppoint.CommitDropResult{
		EnvelopePath:  filepath.ToSlash(filepath.Join(DropPointsDirName, id, EnvelopeFileName)),
		PayloadPath:   filepath.ToSlash(filepath.Join(DropPointsDirName, id, PayloadFileName)),
		EncryptedSize: size,
	}, nil
}

// ReadEnvelope reads a stored envelope by repository-relative path.
func (s *Store) ReadEnvelope(_ context.Context, relative string) ([]byte, error) {
	path, err := s.Path(relative)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read envelope blob %q: %w", relative, err)
	}
	return data, nil
}

// OpenPayload opens a stored encrypted payload by repository-relative path.
func (s *Store) OpenPayload(_ context.Context, relative string) (io.ReadCloser, error) {
	path, err := s.Path(relative)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open payload blob %q: %w", relative, err)
	}
	return file, nil
}

// DeleteDropPoint removes a drop point blob directory. It is idempotent.
func (s *Store) DeleteDropPoint(_ context.Context, id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.dropDir(id)); err != nil {
		return fmt.Errorf("delete drop point blobs %q: %w", id, err)
	}
	return nil
}

// DropDir returns the absolute directory for id.
func (s *Store) DropDir(id string) string {
	return s.dropDir(id)
}

// Path resolves a repository blob path relative to the store data directory.
func (s *Store) Path(relative string) (string, error) {
	clean := filepath.Clean(relative)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("invalid blob path %q", relative)
	}
	return filepath.Join(s.dataDir, clean), nil
}

func (s *Store) dropDir(id string) string {
	return filepath.Join(s.dataDir, DropPointsDirName, id)
}

func writeFileAtomicPart(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("create envelope temp file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write envelope temp file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync envelope temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close envelope temp file: %w", err)
	}
	return nil
}

func writeStreamAtomicPart(path string, reader io.Reader, maxBytes int64) (int64, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return 0, fmt.Errorf("create payload temp file: %w", err)
	}
	limited := io.LimitReader(reader, maxBytes+1)
	size, copyErr := io.Copy(file, limited)
	syncErr := file.Sync()
	closeErr := file.Close()
	switch {
	case copyErr != nil:
		return 0, fmt.Errorf("write payload temp file: %w", copyErr)
	case size > maxBytes:
		return 0, droppoint.ErrPayloadTooLarge
	case syncErr != nil:
		return 0, fmt.Errorf("sync payload temp file: %w", syncErr)
	case closeErr != nil:
		return 0, fmt.Errorf("close payload temp file: %w", closeErr)
	default:
		return size, nil
	}
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open blob directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return fmt.Errorf("sync blob directory: %w", err)
	}
	return nil
}

func validateID(id string) error {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("invalid drop point id %q", id)
	}
	return nil
}

func tempToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate blob temp token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
