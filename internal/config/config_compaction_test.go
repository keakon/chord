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

func TestDefaultConfigContextReductionThresholds(t *testing.T) {
	cfg := DefaultConfig()
	got := cfg.Context.Reduction
	want := ContextReductionConfig{
		ConfirmAgeTurns:      2,
		ErrorAgeTurns:        3,
		ShellSuccessAgeTurns: 2,
		ReadLikeAgeTurns:     1,
		StaleAgeTurns:        4,
		ShellSuccessBytes:    8000,
		ReadLikeOutputBytes:  4000,
		StaleOutputBytes:     1500,
		MinToolResultsPrune:  8,
	}
	if got != want {
		t.Fatalf("DefaultConfig().Context.Reduction = %+v, want %+v", got, want)
	}
}

func TestLoadConfigFromPathParsesNestedCompactionConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("" +
		"context:\n" +
		"  compaction:\n" +
		"    threshold: 0.75\n" +
		"    preset: codex\n" +
		"    profile: archival\n" +
		"    reserved: 16000\n")
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
	if cfg.Context.Compaction.Reserved != 16000 {
		t.Fatalf("reserved = %d, want 16000", cfg.Context.Compaction.Reserved)
	}
	if cfg.Context.Compaction.Threshold != 0.75 {
		t.Fatalf("compaction.threshold = %v, want 0.75", cfg.Context.Compaction.Threshold)
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

func TestLoadConfigFromPathIgnoresUnknownOutputTokenMax(t *testing.T) {
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
		t.Fatalf("output_token_max should be ignored, got max_output_tokens = %d", cfg.MaxOutputTokens)
	}
}

func TestModelLimitEffectiveInputBudget(t *testing.T) {
	const defaultOutputCap = 32000

	cases := []struct {
		name             string
		limit            ModelLimit
		outputCapSetting int
		want             int
	}{
		{
			name:             "explicit input wins",
			limit:            ModelLimit{Context: 400000, Input: 272000, Output: 128000},
			outputCapSetting: 0,
			want:             272000,
		},
		{
			name:             "default output reserved from context",
			limit:            ModelLimit{Context: 400000, Output: 128000},
			outputCapSetting: 0,
			want:             368000,
		},
		{
			name:             "configured output cap reserved from context",
			limit:            ModelLimit{Context: 400000, Output: 128000},
			outputCapSetting: 8192,
			want:             391808,
		},
		{
			name:             "model output cap bounds reservation",
			limit:            ModelLimit{Context: 400000, Output: 4096},
			outputCapSetting: 8192,
			want:             395904,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.limit.EffectiveInputBudget(tc.outputCapSetting, defaultOutputCap); got != tc.want {
				t.Fatalf("EffectiveInputBudget() = %d, want %d", got, tc.want)
			}
		})
	}
}
