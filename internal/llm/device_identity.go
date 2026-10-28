package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func readDeviceIDFile(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// persistDeviceID adopts the first writer's value rather than replacing it.
// On failure the caller may keep id for this process, with a diagnostic.
func persistDeviceID(path, id string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return id, fmt.Errorf("create device id directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// O_EXCL can observe a concurrent writer before its content lands.
		for range 50 {
			if existing := readDeviceIDFile(path); existing != "" {
				return existing, nil
			}
			time.Sleep(2 * time.Millisecond)
		}
		return id, fmt.Errorf("create device id file: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return id, fmt.Errorf("write device id file: %w", err)
	}
	if err := f.Close(); err != nil {
		return id, fmt.Errorf("close device id file: %w", err)
	}
	return id, nil
}
