package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

type configMutationLock struct {
	file *os.File
	path string
}

func (l *configMutationLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	if runtime.GOOS != "windows" {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	}
	err := l.file.Close()
	if err == nil && l.path != "" {
		if removeErr := os.Remove(l.path); removeErr != nil && !os.IsNotExist(removeErr) {
			err = removeErr
		}
	}
	l.file = nil
	l.path = ""
	return err
}

func LockConfigMutation(targetPath string) (*configMutationLock, error) {
	if targetPath == "" {
		return nil, fmt.Errorf("config path is empty")
	}
	lockPath := targetPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create config lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock config file: %w", err)
		}
	}
	return &configMutationLock{file: f, path: lockPath}, nil
}

func WriteConfigFileAtomically(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("set config temp permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write config temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync config temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close config temp file: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check config path %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr == nil {
			return os.ErrExist
		}
		return fmt.Errorf("install config file: %w", err)
	}
	return nil
}

func writeConfigFileAtomicallyReplace(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(mode); err != nil {
		return fmt.Errorf("set config temp permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write config temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync config temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close config temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config file: %w", err)
	}
	return nil
}
