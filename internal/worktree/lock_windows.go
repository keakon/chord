//go:build windows

package worktree

import (
	"fmt"
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const stateFileLockAllBytes = math.MaxUint32

func lockStateFile(lockPath string) (*stateFileLock, error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, stateFileLockAllBytes, stateFileLockAllBytes, ol); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock file %s: %w", lockPath, err)
	}
	return &stateFileLock{file: f}, nil
}

func (l *stateFileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	var firstErr error
	ol := new(windows.Overlapped)
	if err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, stateFileLockAllBytes, stateFileLockAllBytes, ol); err != nil {
		firstErr = err
	}
	if err := l.file.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.file = nil
	return firstErr
}
