package config

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	dataDirMode = 0o700
	blobDirName = "drop-points"
)

// EnsureDataDir creates the relay data directory and known subdirectories with
// owner-only permissions. Existing directories are tightened to the same mode.
func EnsureDataDir(path string) error {
	if path == "" {
		return fmt.Errorf("data directory path must not be empty")
	}

	cleanPath := filepath.Clean(path)
	if err := ensureDirectory(cleanPath, dataDirMode); err != nil {
		return err
	}
	if err := ensureDirectory(filepath.Join(cleanPath, blobDirName), dataDirMode); err != nil {
		return err
	}
	return nil
}

func ensureDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create directory %q: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("set permissions on directory %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	return nil
}
