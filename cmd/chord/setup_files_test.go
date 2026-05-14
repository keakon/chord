package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteInitialConfigFileWritesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("providers:\n  openai:\n    type: responses\n")
	if err := writeInitialConfigFile(path, data); err != nil {
		t.Fatalf("writeInitialConfigFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content = %q, want %q", got, data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Fatalf("config mode = %o, want 644", mode)
	}
	if err := writeInitialConfigFile(path, []byte("other")); err == nil {
		t.Fatal("expected existing config write to fail")
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after second write: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("file content changed: %q", got)
	}
}
