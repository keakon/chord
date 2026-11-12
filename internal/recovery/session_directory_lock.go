package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The open root keeps guard and metadata operations on the claimed directory
// even after it is renamed. No operation on an existing session creates a
// directory or follows a replacement at the original path.
type sessionDirectoryLock struct {
	root  *os.Root
	guard *os.File
	info  os.FileInfo
}

func acquireSessionDirectoryLock(dir string) (*sessionDirectoryLock, error) {
	lock, err := openSessionDirectoryLock(dir)
	if err != nil {
		return nil, err
	}
	if err := lockSessionGuardFile(lock.guard); err != nil {
		_ = lock.guard.Close()
		_ = lock.root.Close()
		if isWouldBlockError(err) {
			return nil, currentSessionLockedError(dir, filepath.Join(dir, sessionLockFile))
		}
		return nil, fmt.Errorf("acquire session guard lock: %w", err)
	}
	if err := lock.verify(dir); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func openSessionDirectoryLock(dir string) (*sessionDirectoryLock, error) {
	pathInfo, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat session directory: %w", err)
	}
	if !pathInfo.IsDir() {
		return nil, fmt.Errorf("session path is not a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open session directory: %w", err)
	}
	info, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("stat session directory: %w", err)
	}
	if !os.SameFile(pathInfo, info) {
		_ = root.Close()
		return nil, fmt.Errorf("session directory was replaced: %w", os.ErrNotExist)
	}
	guard, err := openSessionLockFile(root, sessionGuardFile, os.O_CREATE|os.O_RDWR)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open session guard lock: %w", err)
	}
	return &sessionDirectoryLock{root: root, guard: guard, info: info}, nil
}

func (l *sessionDirectoryLock) verify(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("verify session directory: %w", err)
	}
	if !info.IsDir() || !os.SameFile(l.info, info) {
		return fmt.Errorf("session directory was replaced: %w", os.ErrNotExist)
	}
	guardInfo, err := l.guard.Stat()
	if err != nil {
		return fmt.Errorf("stat held session guard: %w", err)
	}
	pathInfo, err := l.root.Lstat(sessionGuardFile)
	if err != nil {
		return fmt.Errorf("verify session guard: %w", err)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(guardInfo, pathInfo) {
		return fmt.Errorf("session guard was replaced: %w", os.ErrNotExist)
	}
	return nil
}

func (l *sessionDirectoryLock) Close() error {
	return errors.Join(unlockAndCloseGuard(l.guard), l.root.Close())
}

func openSessionLockFile(root *os.Root, name string, flags int) (*os.File, error) {
	if info, err := root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("session lock file %s is not a regular file", name)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return root.OpenFile(name, flags, privateSessionLockMode)
}

const privateSessionLockMode = 0o600
