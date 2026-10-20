package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func backendAgentConfig() *AgentConfig {
	return &AgentConfig{
		Name: "claude-reviewer",
		Mode: AgentModeSubAgent,
		Backend: &BackendConfig{
			Kind:    BackendKindClaudeCode,
			Command: "claude",
			LaunchArgs: []string{
				"--model", "sample/test-model",
				"--max-turns", "12",
			},
		},
	}
}

func TestBackendConfigDefaults(t *testing.T) {
	var backend BackendConfig
	if got := backend.EffectiveCommand(); got != DefaultClaudeCodeCommand {
		t.Fatalf("EffectiveCommand() = %q, want %q", got, DefaultClaudeCodeCommand)
	}
}

func TestValidateAgentBackendAcceptsClaudeCode(t *testing.T) {
	cfg := backendAgentConfig()
	cfg.Backend.Env = map[string]string{"ANTHROPIC_API_KEY": "${SAMPLE_TOKEN}"}
	cfg.Backend.LaunchArgs = append(cfg.Backend.LaunchArgs, "--disallowedTools", "Edit,Write")
	if err := validateAgentBackend("claude-reviewer.md", cfg); err != nil {
		t.Fatalf("validateAgentBackend() error = %v, want nil", err)
	}
}

func TestValidateAgentBackendRejectsNoOpAndConflicts(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*AgentConfig)
		wantErr string
	}{
		{
			name:    "missing kind",
			mutate:  func(c *AgentConfig) { c.Backend.Kind = "" },
			wantErr: "backend.kind is required",
		},
		{
			name:    "unknown kind",
			mutate:  func(c *AgentConfig) { c.Backend.Kind = "codex" },
			wantErr: "unknown backend.kind",
		},
		{
			name:    "main agent backend",
			mutate:  func(c *AgentConfig) { c.Mode = AgentModeMain },
			wantErr: "only supported for subagent definitions",
		},
		{
			name:    "model pools",
			mutate:  func(c *AgentConfig) { c.ModelPools = []string{"default"} },
			wantErr: "must not declare model_pools",
		},
		{
			name:    "mcp",
			mutate:  func(c *AgentConfig) { c.MCP = MCPConfig{"sample": {}} },
			wantErr: "must not declare mcp",
		},
		{
			name:    "prompt preset",
			mutate:  func(c *AgentConfig) { c.PromptPreset = PromptPresetPlanning },
			wantErr: "must not declare prompt_preset",
		},
		{
			name:    "variant",
			mutate:  func(c *AgentConfig) { c.Variant = "high" },
			wantErr: "must not declare variant",
		},
		{
			name:    "temperature",
			mutate:  func(c *AgentConfig) { c.Temperature = 0.2 },
			wantErr: "must not declare variant",
		},
		{
			name:    "max tokens",
			mutate:  func(c *AgentConfig) { c.MaxTokens = 4096 },
			wantErr: "must not declare variant",
		},
		{
			name:    "empty env key",
			mutate:  func(c *AgentConfig) { c.Backend.Env = map[string]string{"": "x"} },
			wantErr: "env key must not be empty",
		},
		{
			name:    "env key with equals sign",
			mutate:  func(c *AgentConfig) { c.Backend.Env = map[string]string{"A=B": "x"} },
			wantErr: "must not contain '='",
		},
		{
			name:    "reserved print flag",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"-p"} },
			wantErr: "not supported",
		},
		{
			name:    "reserved output format",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"--output-format=json"} },
			wantErr: "not supported",
		},
		{
			name: "reserved session id",
			mutate: func(c *AgentConfig) {
				c.Backend.LaunchArgs = []string{"--session-id", "00000000-0000-4000-8000-000000000000"}
			},
			wantErr: "not supported",
		},
		{
			name:    "reserved permission prompt tool",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"--permission-prompt-tool", "stdio"} },
			wantErr: "not supported",
		},
		{
			name:    "permission mode bypass without opt-in",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"--permission-mode", "acceptEdits"} },
			wantErr: "not supported",
		},
		{
			name:    "permission mode equals bypass without opt-in",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"--permission-mode=bypassPermissions"} },
			wantErr: "not supported",
		},
		{
			name:    "dangerously skip permissions without opt-in",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"--dangerously-skip-permissions"} },
			wantErr: "not supported",
		},
		{
			name:    "allowed tools without opt-in",
			mutate:  func(c *AgentConfig) { c.Backend.LaunchArgs = []string{"--allowedTools=Read"} },
			wantErr: "not supported",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := backendAgentConfig()
			tc.mutate(cfg)
			err := validateAgentBackend("claude-reviewer.md", cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateAgentBackend() error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestBackendRemovedFieldsRejected(t *testing.T) {
	for _, field := range []string{
		"min_version: 2.1.88", "permission: {allow_bypass: true}",
		"session: {pin_session_id: false}", "session: {unknown: true}", "unknown: true",
	} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reviewer.yaml")
			if err := os.WriteFile(path, []byte("mode: subagent\nbackend:\n  kind: claude-code\n  "+field+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadAgentConfig(path); err == nil || !strings.Contains(err.Error(), "unsupported backend") {
				t.Fatalf("LoadAgentConfig() = %v", err)
			}
		})
	}
}

func TestValidateAgentBackendIgnoresInternalAgents(t *testing.T) {
	cfg := &AgentConfig{Name: "reviewer", Mode: AgentModeSubAgent}
	if err := validateAgentBackend("reviewer.md", cfg); err != nil {
		t.Fatalf("validateAgentBackend() error = %v, want nil", err)
	}
}

func TestLoadAgentConfigWithBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude-reviewer.md")
	content := `---
name: claude-reviewer
description: Delegates a review task to an external Claude Code session.
mode: subagent
backend:
  kind: claude-code
  command: claude
  launch_args:
    - --model
    - sample/test-model
  env:
    ANTHROPIC_API_KEY: ${SAMPLE_TOKEN}
---
You are a review sub-agent running inside Claude Code.
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig() error = %v", err)
	}
	if !cfg.IsExternalBackend() {
		t.Fatal("IsExternalBackend() = false, want true")
	}
	if got := cfg.Backend.EffectiveCommand(); got != "claude" {
		t.Fatalf("EffectiveCommand() = %q, want claude", got)
	}
	if len(cfg.Backend.LaunchArgs) != 2 || cfg.Backend.LaunchArgs[0] != "--model" || cfg.Backend.LaunchArgs[1] != "sample/test-model" {
		t.Fatalf("LaunchArgs = %v, want [--model sample/test-model]", cfg.Backend.LaunchArgs)
	}
	if got := cfg.Backend.Env["ANTHROPIC_API_KEY"]; got != "${SAMPLE_TOKEN}" {
		t.Fatalf("Env[ANTHROPIC_API_KEY] = %q, want raw reference", got)
	}
	if !strings.Contains(cfg.SystemPrompt, "review sub-agent") {
		t.Fatalf("SystemPrompt = %q, want markdown body", cfg.SystemPrompt)
	}
}

func TestResolveAgentModelPoolsSkipsExternalBackend(t *testing.T) {
	external := backendAgentConfig()
	internal := &AgentConfig{
		Name: "reviewer",
		Mode: AgentModeSubAgent,
		Models: map[string][]string{
			"default": {"sample/test-model"},
		},
	}
	agents := map[string]*AgentConfig{
		external.Name: external,
		internal.Name: internal,
	}
	if err := ResolveAgentModelPools(agents, nil); err != nil {
		t.Fatalf("ResolveAgentModelPools() error = %v, want nil", err)
	}
	if len(external.Models) != 0 {
		t.Fatalf("external backend Models = %v, want empty", external.Models)
	}
	if len(internal.Models["default"]) != 1 {
		t.Fatalf("internal agent Models = %v, want preserved", internal.Models)
	}
}
