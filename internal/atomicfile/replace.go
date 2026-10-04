// Package atomicfile replaces complete files without exposing partial contents.
package atomicfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Replace writes in the target directory, syncs, and renames the complete file.
// Callers serialize writers when a read-modify-write operation requires it.
func Replace(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("file path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create file directory: %w", err)
	}
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("set file permissions: %w", err)
	}
	if n, err := file.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	} else if n != len(data) {
		return fmt.Errorf("write temporary file: %w", io.ErrShortWrite)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	return nil
}
