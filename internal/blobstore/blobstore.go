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
	"syscall"

	"github.com/shunichironomura/droppoint/internal/droppoint"
	"github.com/shunichironomura/droppoint/internal/token"
)

const (
	DropPointsDirName = "drop-points"
	PayloadFileName   = "payload.bin"
	EnvelopeFileName  = "envelope.json"
	fileMode          = 0o600
	dirMode           = 0o700
)

// FailureClass is the HTTP-relevant class of a blob operation failure.
type FailureClass uint8

const (
	FailureInternal FailureClass = iota
	FailureClientInput
	FailureCapacity
	FailureUnavailable
)

// ErrSourceRead marks an error received while reading an uploader-controlled
// stream rather than writing durable storage.
var ErrSourceRead = errors.New("upload source read failed")

// ClassifyFailure maps wrapped filesystem and source-reader failures without
// exposing filesystem paths at the HTTP boundary.
func ClassifyFailure(err error) FailureClass {
	switch {
	case errors.Is(err, ErrSourceRead):
		return FailureClientInput
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return FailureCapacity
	case errors.Is(err, syscall.EAGAIN), errors.Is(err, syscall.EBUSY), errors.Is(err, syscall.ESTALE), errors.Is(err, syscall.ETIMEDOUT), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return FailureUnavailable
	default:
		return FailureInternal
	}
}

type mutationFileSystem interface {
	MkdirAll(path string, mode os.FileMode) error
	Chmod(path string, mode os.FileMode) error
	OpenFile(path string, flag int, mode os.FileMode) (*os.File, error)
	Rename(oldPath string, newPath string) error
	Remove(path string) error
	RemoveAll(path string) error
	SyncDir(path string) error
}

type osMutationFileSystem struct{}

func (osMutationFileSystem) MkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}
func (osMutationFileSystem) Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
func (osMutationFileSystem) OpenFile(path string, flag int, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag, mode)
}
func (osMutationFileSystem) Rename(oldPath string, newPath string) error {
	return os.Rename(oldPath, newPath)
}
func (osMutationFileSystem) Remove(path string) error    { return os.Remove(path) }
func (osMutationFileSystem) RemoveAll(path string) error { return os.RemoveAll(path) }
func (osMutationFileSystem) SyncDir(path string) error   { return syncDir(path) }

// Store writes encrypted payload and envelope blobs under dataDir/drop-points.
type Store struct {
	dataDir string
	fs      mutationFileSystem
}

// New returns a filesystem blob store rooted at dataDir.
func New(dataDir string) *Store {
	return &Store{dataDir: dataDir, fs: osMutationFileSystem{}}
}

// WriteDrop atomically stores envelope.json and payload.bin for a drop point.
func (s *Store) WriteDrop(ctx context.Context, id string, envelope []byte, payload io.Reader, maxBytes int64) (droppoint.CommitDropResult, error) {
	if err := contextError(ctx); err != nil {
		return droppoint.CommitDropResult{}, err
	}
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
	parentDir := filepath.Join(s.dataDir, DropPointsDirName)
	if err := s.fs.MkdirAll(dir, dirMode); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("create drop blob directory %q: %w", dir, err)
	}
	if err := s.fs.Chmod(dir, dirMode); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("set drop blob directory permissions %q: %w", dir, err)
	}
	if err := s.fs.SyncDir(parentDir); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("sync drop-points parent after directory creation: %w", err)
	}
	if err := contextError(ctx); err != nil {
		return droppoint.CommitDropResult{}, err
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
		_ = s.fs.Remove(envelopeTemp)
		_ = s.fs.Remove(payloadTemp)
	}
	defer cleanup()

	if err := writeFileAtomicPart(ctx, s.fs, envelopeTemp, envelope); err != nil {
		return droppoint.CommitDropResult{}, err
	}
	size, err := writeStreamAtomicPart(ctx, s.fs, payloadTemp, payload, maxBytes)
	if err != nil {
		return droppoint.CommitDropResult{}, err
	}
	if err := s.fs.Rename(payloadTemp, payloadFinal); err != nil {
		return droppoint.CommitDropResult{}, fmt.Errorf("install payload blob: %w", err)
	}
	if err := s.fs.Rename(envelopeTemp, envelopeFinal); err != nil {
		_ = s.fs.Remove(payloadFinal)
		return droppoint.CommitDropResult{}, fmt.Errorf("install envelope blob: %w", err)
	}
	if err := s.fs.SyncDir(dir); err != nil {
		return droppoint.CommitDropResult{}, err
	}
	if err := contextError(ctx); err != nil {
		return droppoint.CommitDropResult{}, err
	}

	return droppoint.CommitDropResult{
		EnvelopePath:  filepath.ToSlash(filepath.Join(DropPointsDirName, id, EnvelopeFileName)),
		PayloadPath:   filepath.ToSlash(filepath.Join(DropPointsDirName, id, PayloadFileName)),
		EncryptedSize: size,
	}, nil
}

// ReadEnvelope reads a stored envelope by repository-relative path.
func (s *Store) ReadEnvelope(ctx context.Context, relative string) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	path, err := s.Path(relative)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read envelope blob %q: %w", relative, err)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return data, nil
}

// OpenPayload opens a stored encrypted payload by repository-relative path.
func (s *Store) OpenPayload(ctx context.Context, relative string) (io.ReadCloser, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	path, err := s.Path(relative)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open payload blob %q: %w", relative, err)
	}
	return &contextReadCloser{ctx: ctx, ReadCloser: file}, nil
}

// DeleteDropPoint removes a drop point blob directory. It is idempotent.
func (s *Store) DeleteDropPoint(ctx context.Context, id string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateID(id); err != nil {
		return err
	}
	if err := s.fs.RemoveAll(s.dropDir(id)); err != nil {
		return fmt.Errorf("delete drop point blobs %q: %w", id, err)
	}
	if err := s.fs.SyncDir(filepath.Join(s.dataDir, DropPointsDirName)); err != nil {
		return fmt.Errorf("sync drop-points parent after deleting %q: %w", id, err)
	}
	return contextError(ctx)
}

// DropPointIDs lists receiver-owned drop point blob directories. Entries that
// could not have been created by this store are ignored rather than passed to
// the deletion boundary.
func (s *Store) DropPointIDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.dataDir, DropPointsDirName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list drop point blob directories: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || validateID(entry.Name()) != nil {
			continue
		}
		ids = append(ids, entry.Name())
	}
	return ids, nil
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

func writeFileAtomicPart(ctx context.Context, fs mutationFileSystem, path string, data []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	file, err := fs.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("create envelope temp file: %w", err)
	}
	written, err := file.Write(data)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("write envelope temp file: %w", err)
	}
	if written != len(data) {
		_ = file.Close()
		return fmt.Errorf("write envelope temp file: %w", io.ErrShortWrite)
	}
	if err := contextError(ctx); err != nil {
		_ = file.Close()
		return err
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

func writeStreamAtomicPart(ctx context.Context, fs mutationFileSystem, path string, reader io.Reader, maxBytes int64) (int64, error) {
	if err := contextError(ctx); err != nil {
		return 0, err
	}
	file, err := fs.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return 0, fmt.Errorf("create payload temp file: %w", err)
	}
	size, copyErr := copyPayload(ctx, file, reader, maxBytes)
	syncErr := file.Sync()
	closeErr := file.Close()
	switch {
	case copyErr != nil:
		return 0, copyErr
	case syncErr != nil:
		return 0, fmt.Errorf("sync payload temp file: %w", syncErr)
	case closeErr != nil:
		return 0, fmt.Errorf("close payload temp file: %w", closeErr)
	default:
		return size, nil
	}
}

func copyPayload(ctx context.Context, file *os.File, reader io.Reader, maxBytes int64) (int64, error) {
	buffer := make([]byte, 32*1024)
	var size int64
	for {
		if err := contextError(ctx); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrSourceRead, err)
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if int64(n) > maxBytes-size {
				return 0, droppoint.ErrPayloadTooLarge
			}
			written, writeErr := file.Write(buffer[:n])
			if writeErr != nil {
				return 0, fmt.Errorf("write payload temp file: %w", writeErr)
			}
			if written != n {
				return 0, fmt.Errorf("write payload temp file: %w", io.ErrShortWrite)
			}
			size += int64(written)
		}
		switch {
		case errors.Is(readErr, io.EOF):
			return size, nil
		case readErr != nil:
			return 0, fmt.Errorf("%w: %w", ErrSourceRead, readErr)
		case n == 0:
			return 0, fmt.Errorf("%w: reader returned no data or error", ErrSourceRead)
		}
	}
}

type contextReadCloser struct {
	ctx context.Context
	io.ReadCloser
}

func (r *contextReadCloser) Read(p []byte) (int, error) {
	if err := contextError(r.ctx); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
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
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("invalid drop point id %q", id)
	}
	if !strings.HasPrefix(id, token.DropPointIDPrefix) {
		return fmt.Errorf("invalid drop point id %q", id)
	}
	secret := strings.TrimPrefix(id, token.DropPointIDPrefix)
	if secret == "" {
		return fmt.Errorf("invalid drop point id %q", id)
	}
	for _, r := range secret {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
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
