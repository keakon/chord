package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeEmulationEnabledRequiresTheFullPayload(t *testing.T) {
	complete := ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}
	tests := []struct {
		name    string
		block   *ClaudeEmulationConfig
		enabled bool
	}{
		{"absent block", nil, false},
		{"complete payload", &complete, true},
		{"missing version", &ClaudeEmulationConfig{SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}, false},
		{"missing sdk version", &ClaudeEmulationConfig{Version: "2.1.311", RuntimeVersion: "v26.12.1"}, false},
		{"missing runtime version", &ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.block.ClaudeEmulationEnabled(); got != tc.enabled {
				t.Fatalf("ClaudeEmulationEnabled = %v, want %v", got, tc.enabled)
			}
		})
	}
}

func TestApplyEmulationWiresTheClaudeSelector(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"relay": {
				Type:    ProviderTypeMessages,
				Emulate: "claude",
				Models:  map[string]ModelConfig{"test-model": {}},
			},
		},
		Emulation: EmulationConfig{Claude: &ClaudeEmulationConfig{Version: " 2.1.311 ", SDKVersion: " 0.128.0 ", RuntimeVersion: "\tv26.12.1 "}},
	}
	if diags := ApplyEmulation(cfg); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none for a valid selector", diags)
	}
	payload := cfg.Providers["relay"].Emulation.Claude
	if payload == nil || payload.Version != "2.1.311" || payload.SDKVersion != "0.128.0" || payload.RuntimeVersion != "v26.12.1" {
		t.Fatalf("attached payload = %+v, want the definitions with trimmed version fields", payload)
	}
}

func TestApplyEmulationLetsAModelInheritTheProviderSelector(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"relay": {
				Type:    ProviderTypeMessages,
				Emulate: "claude",
				Models: map[string]ModelConfig{
					"inheriting": {},
					"plain":      {Emulate: " "},
				},
			},
		},
		Emulation: EmulationConfig{Claude: &ClaudeEmulationConfig{Version: "2.1.311", SDKVersion: "0.128.0", RuntimeVersion: "v26.12.1"}},
	}
	if diags := ApplyEmulation(cfg); len(diags) != 0 {
		t.Fatalf("diagnostics = %v, want none", diags)
	}
}

func TestLoadConfigFromPathWiresTheClaudeSelector(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	writeTestFile(t, path, `emulation:
  claude:
    version: 2.1.311
    sdk_version: 0.128.0
    runtime_version: v26.12.1
providers:
  relay:
    type: messages
    emulate: claude
    api_url: https://example.invalid/v1/messages
`)
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	provider := cfg.Providers["relay"]
	if provider.Emulate != "claude" || provider.Emulation == nil || provider.Emulation.Claude == nil {
		t.Fatalf("provider emulate = %q, payload = %+v, want the selector and payload wired", provider.Emulate, provider.Emulation)
	}
}

func TestLoadConfigFromPathWarnsOnAnIncompleteClaudePayload(t *testing.T) {
	_, logs := loadConfigFromPathAndCaptureLog(t, `emulation:
  claude:
    version: 2.1.311
providers:
  relay:
    type: messages
    emulate: claude
    api_url: https://example.invalid/v1/messages
`)
	if !strings.Contains(logs, "missing or incomplete") {
		t.Fatalf("logs = %q, want a warning about the incomplete payload", logs)
	}
}

func TestEmulationNamespaceIsProjectScoped(t *testing.T) {
	if !projectScopedTopLevelKeys["emulation"] {
		t.Fatal("emulation must be project-scoped so a project layer can override it")
	}
	if projectScopedTopLevelKeys["codex_emulation"] || projectScopedTopLevelKeys["claude_emulation"] {
		t.Fatal("the per-client keys are sub-blocks of emulation now and must not be whitelisted")
	}
}

func TestValidateHostedToolHeadersKeepsClaudeIdentityManaged(t *testing.T) {
	for _, name := range []string{
		"x-claude-code-session-id",
		"x-stainless-lang", "x-stainless-runtime", "x-stainless-os", "x-stainless-arch",
		"x-stainless-package-version", "x-stainless-runtime-version",
		"anthropic-dangerous-direct-browser-access",
	} {
		if err := ValidateHostedToolHeaders(map[string]string{name: "override"}); err == nil {
			t.Errorf("hosted tool header %q was accepted, want it rejected as provider-managed", name)
		}
	}
	// A per-request retry counter and timeout budget are transport facts a
	// declaration may tune.
	for _, name := range []string{"x-stainless-retry-count", "x-stainless-timeout"} {
		if err := ValidateHostedToolHeaders(map[string]string{name: "1"}); err != nil {
			t.Errorf("hosted tool header %q was rejected: %v", name, err)
		}
	}
	if err := ValidateHostedToolHeaders(map[string]string{"x-unknown": "value"}); err != nil {
		t.Fatalf("unrelated header was rejected: %v", err)
	}
}

func TestClaudeDeviceIDPathSitsBesideCodexDeviceID(t *testing.T) {
	path, err := ClaudeDeviceIDPath()
	if err != nil {
		t.Fatalf("ClaudeDeviceIDPath: %v", err)
	}
	codexPath, err := CodexDeviceIDPath()
	if err != nil {
		t.Fatalf("CodexDeviceIDPath: %v", err)
	}
	if filepath.Dir(path) != filepath.Dir(codexPath) {
		t.Fatalf("claude device id path = %q, want it in the codex device id directory %q", path, filepath.Dir(codexPath))
	}
	if filepath.Base(path) != ClaudeDeviceIDFileName {
		t.Fatalf("claude device id file name = %q, want %q", filepath.Base(path), ClaudeDeviceIDFileName)
	}
}
