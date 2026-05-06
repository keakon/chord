package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelPoolStatePath(t *testing.T) {
	path := ModelPoolStatePath("my-project", "/state")
	expected := filepath.Join("/state", "projects", "my-project", "model_pool_state.yaml")
	if path != expected {
		t.Fatalf("got %q, want %q", path, expected)
	}
}

func TestLoadModelPoolStateNotExist(t *testing.T) {
	state, err := LoadModelPoolState("/nonexistent/path/state.yaml")
	if err != nil {
		t.Fatalf("LoadModelPoolState non-existent: %v", err)
	}
	if state.Version != modelPoolStateVersion {
		t.Fatalf("version: got %d, want %d", state.Version, modelPoolStateVersion)
	}
	if state.CurrentRole != "" {
		t.Fatalf("global: got %q, want empty", state.CurrentRole)
	}
}

func TestSaveAndLoadModelPoolState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_pool_state.yaml")

	state := &ModelPoolState{
		CurrentRole: "fast",
		AgentOverrides: map[string]string{
			"reviewer": "strong",
		},
	}

	if err := SaveModelPoolState(path, state); err != nil {
		t.Fatalf("SaveModelPoolState: %v", err)
	}

	loaded, err := LoadModelPoolState(path)
	if err != nil {
		t.Fatalf("LoadModelPoolState: %v", err)
	}
	if loaded.CurrentRole != "fast" {
		t.Fatalf("global: got %q, want %q", loaded.CurrentRole, "fast")
	}
	if loaded.AgentOverrides["reviewer"] != "strong" {
		t.Fatalf("reviewer override: got %q, want %q", loaded.AgentOverrides["reviewer"], "strong")
	}
}

func TestSaveModelPoolStateAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_pool_state.yaml")

	state := &ModelPoolState{CurrentRole: "default"}
	if err := SaveModelPoolState(path, state); err != nil {
		t.Fatalf("SaveModelPoolState: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temp file should not remain: %s", e.Name())
		}
	}
}

func TestLoadModelPoolStateCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_pool_state.yaml")
	if err := os.WriteFile(path, []byte("{{corrupt yaml"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	state, err := LoadModelPoolState(path)
	if err != nil {
		t.Fatalf("LoadModelPoolState corrupt file should not error: %v", err)
	}
	if state.Version != modelPoolStateVersion {
		t.Fatalf("corrupt file should return default state, version: got %d", state.Version)
	}
}

func TestSaveModelPoolStateNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "model_pool_state.yaml")
	if err := SaveModelPoolState(path, nil); err != nil {
		t.Fatalf("SaveModelPoolState nil should not error: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("nil state should not create a file")
	}
}
