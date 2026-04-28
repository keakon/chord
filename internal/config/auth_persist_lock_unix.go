//go:build unix

package config

import (
	"fmt"
	"os"
	"syscall"
)

func lockAuthYAMLFile(path string) (*authYAMLLock, error) {
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open auth config lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
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
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		firstErr = err
	}
	if err := l.file.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	l.file = nil
	return firstErr
}
