//go:build windows

package config

import (
	"fmt"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const authYAMLLockAllBytes = math.MaxUint32

func lockAuthYAMLFile(path string) (*authYAMLLock, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open auth config lock: %w", err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, authYAMLLockAllBytes, authYAMLLockAllBytes, ol); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock auth config: %w", err)
	}
	return &authYAMLLock{file: f}, nil
}

func (l *authYAMLLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	var firstErr error
	ol := new(windows.Overlapped)
	if err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, authYAMLLockAllBytes, authYAMLLockAllBytes, ol); err != nil {
		firstErr = err
	}
	if err := l.file.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.file = nil
	return firstErr
}
