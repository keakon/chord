package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeInitialConfigFile(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config home: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; rerun chord to continue", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check config path %s: %w", path, err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create config temp file: %w", err)
	}
	tmpPath := f.Name()
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := f.Chmod(0o644); err != nil {
		return fmt.Errorf("set config temp permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write config temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close config temp file: %w", err)
	}
	if err := os.Link(tmpPath, path); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; rerun chord to continue", path)
		}
		return fmt.Errorf("install config file: %w", err)
	}
	return nil
}
