package config

import (
	"strings"
	"testing"
)

func writeCodexEmulationConfig(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	writeTestFile(t, path, content)
	return path
}

func TestApplyEmulationAttachesPayloadAndKeepsValidSelectors(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"relay": {
				Type:    ProviderTypeResponses,
				Emulate: " codex ",
				Models:  map[string]ModelConfig{"test-model": {Emulate: "codex"}},
			},
		},
		Emulation: EmulationConfig{Codex: &CodexEmulationConfig{Version: " 0.50.0 "}},
	}
	if diags := ApplyEmulation(cfg); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none for a valid selector", diags)
	}
	if cfg.Providers["relay"].Emulation == nil || cfg.Providers["relay"].Emulation.Codex.Version != "0.50.0" {
		t.Fatalf("provider emulation payload = %+v, want the attached definitions with trimmed version", cfg.Providers["relay"].Emulation)
	}
}

func TestValidateEmulationRejectsUnusableSelectors(t *testing.T) {
	complete := EmulationConfig{
		Codex:  &CodexEmulationConfig{Version: "0.50.0"},
		Claude: &ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"},
	}
	tests := []struct {
		name     string
		cfg      *Config
		contains string
	}{
		{
			name: "unknown client",
			cfg: &Config{
				Providers: map[string]ProviderConfig{"relay": {Type: ProviderTypeResponses, Emulate: "cursor"}},
				Emulation: complete,
			},
			contains: "unknown emulation client",
		},
		{
			name: "codex on a messages provider",
			cfg: &Config{
				Providers: map[string]ProviderConfig{"relay": {Type: ProviderTypeMessages, Emulate: "codex"}},
				Emulation: complete,
			},
			contains: "only applies to type: responses",
		},
		{
			name: "claude on a responses provider",
			cfg: &Config{
				Providers: map[string]ProviderConfig{"relay": {Type: ProviderTypeResponses, Emulate: "claude"}},
				Emulation: complete,
			},
			contains: "only applies to type: messages",
		},
		{
			name: "missing payload",
			cfg: &Config{
				Providers: map[string]ProviderConfig{"relay": {Type: ProviderTypeResponses, Emulate: "codex"}},
			},
			contains: "missing or incomplete",
		},
		{
			name: "incomplete payload",
			cfg: &Config{
				Providers: map[string]ProviderConfig{"relay": {Type: ProviderTypeMessages, Emulate: "claude"}},
				Emulation: EmulationConfig{Claude: &ClaudeEmulationConfig{Version: "2.1.311"}},
			},
			contains: "missing or incomplete",
		},
		{
			name: "model-level override with a mismatched client",
			cfg: &Config{
				Providers: map[string]ProviderConfig{"relay": {
					Type:    ProviderTypeResponses,
					Emulate: "codex",
					Models:  map[string]ModelConfig{"test-model": {Emulate: "claude"}},
				}},
				Emulation: complete,
			},
			contains: "only applies to type: messages",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diags := ApplyEmulation(tc.cfg)
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %v, want exactly one error", diags)
			}
			if diags[0].Severity != DiagnosticSeverityError || !strings.Contains(diags[0].Message, tc.contains) {
				t.Fatalf("diagnostic = %+v, want an error containing %q", diags[0], tc.contains)
			}
		})
	}
}

func TestValidateEmulationResolvesTheTypeFromAPIURL(t *testing.T) {
	complete := EmulationConfig{
		Codex:  &CodexEmulationConfig{Version: "0.50.0"},
		Claude: &ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"},
	}
	// A provider without type: is judged by the endpoint its api_url implies —
	// or by the codex preset — the same inference the runtime applies, so
	// selectors on type-less providers must not be flagged for an empty string.
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"codex-relay":  {APIURL: "https://example.invalid/v1/responses", Emulate: "codex"},
			"claude-relay": {APIURL: "https://example.invalid/v1/messages", Emulate: "claude"},
			"preset-codex": {Preset: "codex", Emulate: "codex"},
			"mismatched":   {APIURL: "https://example.invalid/v1/messages", Emulate: "codex"},
		},
		Emulation: complete,
	}
	diags := ApplyEmulation(cfg)
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %v, want exactly one type-mismatch error", diags)
	}
	if !strings.Contains(diags[0].Message, "only applies to type: responses") || diags[0].Path != "providers.mismatched.emulate" {
		t.Fatalf("diagnostic = %+v, want the responses-only error on the messages-endpoint provider", diags[0])
	}
}

func TestLoadConfigFromPathAcceptsSelectorsWithoutAnExplicitType(t *testing.T) {
	cfg, logs := loadConfigFromPathAndCaptureLog(t, `emulation:
  codex:
    version: 0.50.0
providers:
  relay:
    emulate: codex
    api_url: https://example.invalid/v1/responses
`)
	if cfg.Providers["relay"].Emulate != "codex" {
		t.Fatalf("provider emulate = %q, want the selector kept", cfg.Providers["relay"].Emulate)
	}
	if strings.Contains(logs, "only applies to") {
		t.Fatalf("logs = %q, want no type-mismatch warning for an api_url-inferred responses provider", logs)
	}
}

func TestApplyEmulationSkipsEmptySelectors(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"plain": {Type: ProviderTypeResponses, Models: map[string]ModelConfig{"test-model": {}}},
		},
	}
	if diags := ApplyEmulation(cfg); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none without an emulate value", diags)
	}
	if cfg.Providers["plain"].Emulation == nil {
		t.Fatal("payload definitions must attach to every provider so selectors can resolve")
	}
}

func TestLoadConfigFromPathWiresTheCodexSelector(t *testing.T) {
	path := writeCodexEmulationConfig(t, `emulation:
  codex:
    version: 0.50.0
providers:
  sample:
    type: responses
    emulate: codex
    api_url: https://example.invalid/v1/responses
`)
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	provider := cfg.Providers["sample"]
	if provider.Emulate != "codex" || provider.Emulation == nil || provider.Emulation.Codex == nil {
		t.Fatalf("provider emulate = %q, payload = %+v, want the selector and payload wired", provider.Emulate, provider.Emulation)
	}
}

func TestLoadConfigFromPathWarnsOnAnUnusableSelector(t *testing.T) {
	_, logs := loadConfigFromPathAndCaptureLog(t, `emulation:
  codex:
    version: 0.50.0
providers:
  sample:
    type: messages
    emulate: codex
    api_url: https://example.invalid/v1/messages
`)
	if !strings.Contains(logs, "only applies to type: responses") {
		t.Fatalf("logs = %q, want a warning about the wire-type mismatch", logs)
	}
}

func TestLoadResolvedConfigSurfacesAnUnusableSelectorAsDiagnostic(t *testing.T) {
	global := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    type: responses
    emulate: codex
    api_url: https://example.invalid/v1/responses
`)
	project := writeOriginLayer(t, "config.yaml", `providers:
  project-provider:
    type: responses
    emulate: codex
    api_url: https://example.invalid/v1/responses
`)
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	found := false
	for _, diag := range rc.Diagnostics {
		if diag.Severity == DiagnosticSeverityError && strings.Contains(diag.Message, "missing or incomplete") {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %v, want an error about the missing codex payload", rc.Diagnostics)
	}
}

func TestLoadResolvedConfigLetsAProjectSelectAClient(t *testing.T) {
	global := writeOriginLayer(t, "config.yaml", `emulation:
  codex:
    version: 0.50.0
  claude:
    version: 2.1.311
    sdk_version: 0.128.0
    runtime_version: v26.12.1
providers:
  sample-provider:
    type: messages
    api_url: https://example.invalid/v1/messages
`)
	project := writeOriginLayer(t, "config.yaml", `providers:
  sample-provider:
    emulate: claude
`)
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	if diags := ApplyEmulation(rc.Config); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
	if got := rc.Config.Providers["sample-provider"].Emulate; got != "claude" {
		t.Fatalf("provider emulate = %q, want the project-level selector", got)
	}
}

func TestLoadConfigFromPathIgnoresProviderLevelEmulationBlock(t *testing.T) {
	cfg, logs := loadConfigFromPathAndCaptureLog(t, `providers:
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    emulation:
      codex:
        version: 0.50.0
`)
	if _, ok := cfg.Providers["sample"]; !ok {
		t.Fatal("LoadConfigFromPath: sample provider missing")
	}
	if got := cfg.Providers["sample"].Emulation.Codex; got != nil {
		t.Fatalf("provider emulation payload = %+v, want nil: payloads are only a top-level key", got)
	}
	if !strings.Contains(logs, "emulation") {
		t.Fatalf("logs = %q, want warning mentioning emulation", logs)
	}
}
