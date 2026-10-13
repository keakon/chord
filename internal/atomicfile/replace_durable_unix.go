//go:build !windows

package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func makeDurableDirectory(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("path is not a directory")
		}
		// A prior attempt may have created this directory but failed to sync
		// its parent. Confirm that entry before relying on it.
		return syncDirectory(filepath.Dir(path))
	}
	if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if err := makeDurableDirectory(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil && !os.IsExist(err) {
		return err
	}
	return syncDirectory(parent)
}

func createDurableTemp(dir, base string, mode os.FileMode) (*os.File, error) {
	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

func commitDurable(source, target string) (ReplaceOutcome, error) {
	return commitDurableWithSync(source, target, syncDirectory)
}

func commitDurableWithSync(source, target string, sync func(string) error) (ReplaceOutcome, error) {
	if err := os.Rename(source, target); err != nil {
		// An I/O failure need not prove that the filesystem left the old
		// directory entry intact.
		if errors.Is(err, syscall.EIO) {
			return ReplacementUnknown, fmt.Errorf("replace file: %w", err)
		}
		return NotReplaced, fmt.Errorf("replace file: %w", err)
	}
	if err := sync(filepath.Dir(target)); err != nil {
		return ReplacementUnknown, fmt.Errorf("sync replacement directory: %w", err)
	}
	return Durable, nil
}
