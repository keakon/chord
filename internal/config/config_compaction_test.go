package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigCompactionProfileDefaultsToAuto(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Context.Compaction.Threshold != DefaultSubAgentCompactUsage {
		t.Fatalf("main compaction threshold = %v, subagent default = %v; defaults should stay aligned", cfg.Context.Compaction.Threshold, DefaultSubAgentCompactUsage)
	}
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
		ConfirmAgeTurns:         2,
		ErrorAgeTurns:           3,
		HighRiskProtectAgeTurns: 4,
		DiffProtectAgeTurns:     12,
		ShellSuccessAgeTurns:    1,
		ReadLikeAgeTurns:        1,
		StaleAgeTurns:           3,
		ShellSuccessBytes:       3000,
		ReadLikeOutputBytes:     3000,
		StaleOutputBytes:        1500,
		WrapUpGraceRequests:     1,
		MinToolResultsPrune:     6,
		MinIncrementalTokens:    2048,
		mode:                    contextReductionEnabled,
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

func TestLoadConfigFromPathContextReductionKeepsDefaultTuning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("context:\n  reduction: {}\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	defaults := DefaultConfig().Context.Reduction
	if cfg.Context.Reduction != defaults {
		t.Fatalf("context.reduction = %+v, want defaults %+v", cfg.Context.Reduction, defaults)
	}
}

func TestLoadConfigFromPathContextReductionTrueUsesDefaultTuning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("context:\n  reduction: true\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	defaults := DefaultConfig().Context.Reduction
	if cfg.Context.Reduction != defaults {
		t.Fatalf("context.reduction = %+v, want defaults %+v", cfg.Context.Reduction, defaults)
	}
}

func TestLoadConfigFromPathContextReductionFalseDisables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("context:\n  reduction: false\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if !cfg.Context.Reduction.DisabledValue() {
		t.Fatal("context.reduction: false should disable request-level reduction")
	}
}

func TestLoadConfigFromPathContextReductionInvalidScalarIsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("context:\n  reduction: sometimes\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadConfigFromPath(path)
	if err == nil {
		t.Fatal("LoadConfigFromPath succeeded, want parse error for context.reduction: sometimes")
	}
	if got := err.Error(); !strings.Contains(got, "expected a mapping or boolean") {
		t.Fatalf("LoadConfigFromPath error = %q, want mapping-or-boolean parse error", got)
	}
}

func TestLoadConfigFromPathRejectsRemovedContextReductionUsageKeys(t *testing.T) {
	for _, key := range []string{"high_pressure_usage", "force_prune_usage"} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			content := []byte("context:\n  reduction:\n    " + key + ": 0.8\n")
			if err := os.WriteFile(path, content, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfigFromPath(path)
			if err == nil || !strings.Contains(err.Error(), key+" was removed") {
				t.Fatalf("LoadConfigFromPath error = %v, want removed-key guidance", err)
			}
		})
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

func TestLoadConfigFromPathRejectsUnknownOutputTokenMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := []byte("output_token_max: 8192\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := LoadConfigFromPath(path); err == nil {
		t.Fatalf("LoadConfigFromPath: expected error for unknown output_token_max, got nil")
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
		{
			// Non-additive published limits (gpt-5.4 shape): 950000 + 128000
			// exceeds the 1050000 window, so the input budget is clamped to
			// context minus the effective requested output.
			name:             "explicit input clamped to context minus output",
			limit:            ModelLimit{Context: 1050000, Input: 950000, Output: 128000},
			outputCapSetting: 128000,
			want:             922000,
		},
		{
			// A smaller requested output leaves room for the full input limit.
			name:             "explicit input fits with small requested output",
			limit:            ModelLimit{Context: 1050000, Input: 950000, Output: 128000},
			outputCapSetting: 8192,
			want:             950000,
		},
		{
			// Degenerate configuration: output consumes the whole window; the
			// clamp still returns a positive budget.
			name:             "clamp floors at one token",
			limit:            ModelLimit{Context: 1000, Input: 900, Output: 1000},
			outputCapSetting: 1000,
			want:             1,
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

func TestLoadConfigDerivesContextFromInputAndOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, `providers:
  relay:
    models:
      text:
        limit:
          input: 200000
          output: 64000
`)

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	limit := cfg.Providers["relay"].Models["text"].Limit
	if limit.Context != 264000 {
		t.Fatalf("derived context = %d, want 264000", limit.Context)
	}
	if limit.Input != 200000 || limit.Output != 64000 {
		t.Fatalf("derived limit changed input/output: %+v", limit)
	}
}

func TestLoadConfigExplicitContextWinsOverInputAndOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, `providers:
  relay:
    models:
      text:
        limit:
          context: 400000
          input: 200000
          output: 64000
`)

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if got := cfg.Providers["relay"].Models["text"].Limit.Context; got != 400000 {
		t.Fatalf("explicit context = %d, want 400000", got)
	}
}
