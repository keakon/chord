package config

import (
	"fmt"
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
model_pools: [default]
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
model_pools: [default]
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

func TestLoadAgentConfigIgnoresRetiredRoutingAnnotations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.yaml")
	content := `name: worker
mode: subagent
model_pools: [default]
capabilities: [edit, test]
preferred_tasks: [feature, bugfix]
write_mode: write
delegation_policy: leaf_preferred
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
model_pools: [default]
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
model_pools: [default]
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

func TestLoadAgentConfigRejectsDelegationLimitsAboveTheCeiling(t *testing.T) {
	tests := []struct {
		name       string
		delegation string
		wantErr    string
	}{
		{
			name:       "max_children above ceiling",
			delegation: fmt.Sprintf("  max_children: %d\n", MaxDelegationMaxChildren+1),
			wantErr:    "delegation.max_children",
		},
		{
			name:       "max_depth above ceiling",
			delegation: fmt.Sprintf("  max_depth: %d\n", MaxDelegationMaxDepth+1),
			wantErr:    "delegation.max_depth",
		},
		{
			name:       "negative max_children",
			delegation: "  max_children: -1\n",
			wantErr:    "delegation.max_children",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "worker.md")
			content := "---\nname: worker\nmode: subagent\nmodel_pools: [default]\ndelegation:\n" + tc.delegation + "---\nBody.\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			_, err := LoadAgentConfig(path)
			if err == nil {
				t.Fatal("LoadAgentConfig() = nil error, want an explicit rejection instead of a silent clamp")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadAgentConfig error = %v, want it to name %s", err, tc.wantErr)
			}
		})
	}
}

func TestDelegationConfigClampsToCeilings(t *testing.T) {
	// Built-in definitions and tests construct DelegationConfig directly, so the
	// ceilings must also hold outside the config loader.
	cfg := DelegationConfig{MaxChildren: MaxDelegationMaxChildren * 10, MaxDepth: MaxDelegationMaxDepth * 10}
	if got := cfg.EffectiveMaxChildren(); got != MaxDelegationMaxChildren {
		t.Fatalf("EffectiveMaxChildren() = %d, want the ceiling %d", got, MaxDelegationMaxChildren)
	}
	if got := cfg.EffectiveMaxDepth(); got != MaxDelegationMaxDepth {
		t.Fatalf("EffectiveMaxDepth() = %d, want the ceiling %d", got, MaxDelegationMaxDepth)
	}
}

func TestDelegationConfigHonoursConfiguredChildrenBelowCeiling(t *testing.T) {
	// A configured fan-out above the default must not be clamped back down to
	// it: the default is a default, not a limit.
	cfg := DelegationConfig{MaxChildren: DefaultDelegationMaxChildren + 5}
	if got := cfg.EffectiveMaxChildren(); got != DefaultDelegationMaxChildren+5 {
		t.Fatalf("EffectiveMaxChildren() = %d, want the configured %d", got, DefaultDelegationMaxChildren+5)
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
