package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/keakon/chord/internal/config"
)

func writeInitialConfigFile(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	lock, err := config.LockConfigMutation(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Close()
	}()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config home: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists; rerun chord to continue", path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check config path %s: %w", path, err)
	}
	if err := config.WriteConfigFileAtomically(path, data, 0o644); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists; rerun chord to continue", path)
		}
		return err
	}
	return nil
}
