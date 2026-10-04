package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateOrUpdateConfigFileLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	rejected := errors.New("invalid config")
	if err := CreateOrUpdateConfigFileLocked(path, func([]byte) ([]byte, error) { return nil, rejected }); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("validation failure created a config: %v", err)
	}
	if err := CreateOrUpdateConfigFileLocked(path, func(current []byte) ([]byte, error) {
		if len(current) != 0 {
			t.Fatal("initial bytes are not empty")
		}
		return []byte("first"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := CreateOrUpdateConfigFileLocked(path, func(current []byte) ([]byte, error) { return append(current, []byte(" second")...), nil }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "first second" {
		t.Fatalf("got %q, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v, %v", info, err)
	}
}
