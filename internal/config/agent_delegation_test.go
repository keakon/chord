package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentConfigParsesDelegationFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.md")
	content := `---
name: worker
mode: subagent
models:
  - sample/test-model
delegation:
  max_children: 3
  max_depth: 2
  child_join: false
---
Custom prompt body.
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if cfg.Delegation.MaxChildren != 3 {
		t.Fatalf("Delegation.MaxChildren = %d, want 3", cfg.Delegation.MaxChildren)
	}
	if cfg.Delegation.MaxDepth != 2 {
		t.Fatalf("Delegation.MaxDepth = %d, want 2", cfg.Delegation.MaxDepth)
	}
	if cfg.Delegation.ChildJoin == nil || *cfg.Delegation.ChildJoin {
		t.Fatalf("Delegation.ChildJoin = %#v, want false", cfg.Delegation.ChildJoin)
	}
}

func TestLoadAgentConfigParsesPlainYAMLDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	content := `name: worker
mode: subagent
models:
  - sample/test-model
prompt: |
  You are a YAML-defined worker.
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if cfg.Name != "worker" {
		t.Fatalf("Name = %q, want worker", cfg.Name)
	}
	if got := cfg.SystemPrompt; got != "You are a YAML-defined worker." {
		t.Fatalf("SystemPrompt = %q, want YAML prompt body", got)
	}
}

func TestLoadAgentConfigParsesPlainYAMLSystemPromptField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yml")
	content := `name: worker
mode: subagent
models:
  - sample/test-model
system_prompt: |
  Use alternate YAML prompt field.
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if got := cfg.SystemPrompt; got != "Use alternate YAML prompt field." {
		t.Fatalf("SystemPrompt = %q, want alternate YAML prompt body", got)
	}
}

func TestLoadAgentConfigRejectsMissingFrontmatterClosingDelimiter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.md")
	content := `---
name: worker
mode: subagent
models:
  - sample/test-model
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := LoadAgentConfig(path)
	if err == nil {
		t.Fatal("expected LoadAgentConfig to reject missing closing delimiter")
	}
	if got := err.Error(); got == "" || !strings.Contains(got, "missing frontmatter closing delimiter") {
		t.Fatalf("LoadAgentConfig error = %v, want missing frontmatter closing delimiter", err)
	}
}

func TestDelegationConfigDefaults(t *testing.T) {
	var cfg DelegationConfig
	if got := cfg.EffectiveMaxChildren(); got != DefaultDelegationMaxChildren {
		t.Fatalf("EffectiveMaxChildren() = %d, want %d", got, DefaultDelegationMaxChildren)
	}
	if got := cfg.EffectiveMaxDepth(); got != DefaultDelegationMaxDepth {
		t.Fatalf("EffectiveMaxDepth() = %d, want %d", got, DefaultDelegationMaxDepth)
	}
	if !cfg.ChildJoinEnabled() {
		t.Fatal("ChildJoinEnabled() = false, want true by default")
	}
}
