package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProviderKeySelectionAcceptsCommonModes(t *testing.T) {
	cases := []ProviderConfig{
		{},
		{KeyRotation: KeyRotationOnFailure, KeyOrder: KeyOrderSequential},
		{KeyRotation: KeyRotationPerRequest, KeyOrder: KeyOrderRandom},
		{Preset: ProviderPresetCodex, KeyOrder: KeyOrderSmart},
	}
	for _, cfg := range cases {
		if err := ValidateProviderKeySelection("p", cfg); err != nil {
			t.Fatalf("ValidateProviderKeySelection(%#v) unexpected error: %v", cfg, err)
		}
	}
}

func TestValidateProviderKeySelectionRejectsInvalidRotation(t *testing.T) {
	err := ValidateProviderKeySelection("p", ProviderConfig{KeyRotation: "per-call"})
	if err == nil {
		t.Fatal("expected invalid key_rotation error")
	}
}

func TestValidateProviderKeySelectionRejectsInvalidOrder(t *testing.T) {
	err := ValidateProviderKeySelection("p", ProviderConfig{KeyOrder: "round_robin"})
	if err == nil {
		t.Fatal("expected invalid key_order error")
	}
}

func TestValidateProviderKeySelectionRejectsSmartForNonCodex(t *testing.T) {
	err := ValidateProviderKeySelection("p", ProviderConfig{KeyOrder: KeyOrderSmart})
	if err == nil {
		t.Fatal("expected key_order smart non-codex error")
	}
}

func TestLoadConfigFromPathMalformedYAMLErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("providers: [\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadConfigFromPath(path); err == nil {
		t.Fatal("malformed global config must fail to load instead of starting with defaults")
	}
}

func TestLoadConfigOverrideFromPathMalformedYAMLErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.yaml")
	if err := os.WriteFile(path, []byte("commands: [\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadConfigOverrideFromPath(path); err == nil {
		t.Fatal("malformed override config must fail to load")
	}
}

// TestLoadConfigResetsInvalidDiagnosticsValues pins the loader's promise that
// invalid diagnostics values are "treated as not configured": offending fields
// are reset to their unset state so the runtime falls back to built-in
// defaults, while valid sibling fields survive.
func TestLoadConfigResetsInvalidDiagnosticsValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `diagnostics:
  python:
    semantic_backend:
      type: command
    quick_backend:
      type: lsp
    large_file:
      line_threshold: -1
      byte_threshold: -5
      strategy: semantic
    output:
      max_total_diagnostics: -3
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	py := cfg.Diagnostics.Python
	if py.SemanticBackend.Type != "" || py.QuickBackend.Type != "" {
		t.Fatalf("backend types = %q/%q, want reset to unset", py.SemanticBackend.Type, py.QuickBackend.Type)
	}
	if py.LargeFile.LineThreshold != 0 || py.LargeFile.ByteThreshold != 0 || py.LargeFile.Strategy != "" {
		t.Fatalf("large file = %+v, want negative/invalid values reset", py.LargeFile)
	}
	if py.Output.MaxTotalDiagnostics != 0 {
		t.Fatalf("max_total_diagnostics = %d, want reset to 0", py.Output.MaxTotalDiagnostics)
	}
}

// TestLoadConfigFromPathResetsInvalidSemanticValues guards the loader's
// promise that an invalid value is "treated as not configured": semantic
// failures must reset the offending field (not ride through to the runtime,
// where they would still fail provider construction or silently skew
// behavior), while valid sibling fields are preserved.
func TestLoadConfigFromPathResetsInvalidSemanticValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`providers:
  sample:
    type: responses
    key_rotation: per-call
    key_order: round_robin
    retry_backoff: linear
    retry_delay_ms: 999999
`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	p := cfg.Providers["sample"]
	if p.KeyRotation != "" || p.KeyOrder != "" || p.RetryBackoff != "" || p.RetryDelayMS != nil {
		t.Fatalf("invalid semantic values not reset: key_rotation=%q key_order=%q retry_backoff=%q retry_delay_ms=%v", p.KeyRotation, p.KeyOrder, p.RetryBackoff, p.RetryDelayMS)
	}
	if p.Type != "responses" {
		t.Fatalf("valid sibling field type = %q, want preserved", p.Type)
	}
}
