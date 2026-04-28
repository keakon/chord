package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigCompactionProfileDefaultsToAuto(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Context.Compaction.Profile != CompactionProfileAuto {
		t.Fatalf("DefaultConfig().Context.Compaction.Profile = %q, want %q", cfg.Context.Compaction.Profile, CompactionProfileAuto)
	}
	if cfg.Context.Compaction.Preset != "" {
		t.Fatalf("DefaultConfig().Context.Compaction.Preset = %q, want empty auto-detect", cfg.Context.Compaction.Preset)
	}
}

func TestLoadConfigFromPathParsesNestedCompactionConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("" +
		"context:\n" +
		"  auto_compact: true\n" +
		"  compact_threshold: 0.75\n" +
		"  compaction:\n" +
		"    preset: codex\n" +
		"    profile: archival\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.Context.Compaction.Preset != CompactionPresetCodex {
		t.Fatalf("preset = %q, want %q", cfg.Context.Compaction.Preset, CompactionPresetCodex)
	}
	if cfg.Context.Compaction.Profile != CompactionProfileArchival {
		t.Fatalf("profile = %q, want %q", cfg.Context.Compaction.Profile, CompactionProfileArchival)
	}
	if cfg.Context.CompactThreshold != 0.75 {
		t.Fatalf("compact_threshold = %v, want 0.75", cfg.Context.CompactThreshold)
	}
}

func TestLoadConfigFromPathParsesMaxOutputTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("max_output_tokens: 8192\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.MaxOutputTokens != 8192 {
		t.Fatalf("max_output_tokens = %d, want 8192", cfg.MaxOutputTokens)
	}
}

func TestLoadConfigFromPathIgnoresLegacyOutputTokenMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("output_token_max: 8192\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.MaxOutputTokens != 0 {
		t.Fatalf("legacy output_token_max should be ignored, got max_output_tokens = %d", cfg.MaxOutputTokens)
	}
}
