package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
)

func TestDefaultPlannerAgentUsesUpdatedPermissionPolicy(t *testing.T) {
	cfg := DefaultPlannerAgent()
	ruleset := permission.ParsePermission(&cfg.Permission)
	checks := []struct {
		perm    string
		pattern string
		want    permission.Action
	}{
		{perm: toolname.Shell, pattern: "go test ./...", want: permission.ActionAllow},
		{perm: toolname.Read, pattern: "internal/agent/main.go", want: permission.ActionAllow},
		{
			perm:    toolname.Write,
			pattern: ".chord/plans/plan-001.md",
			want:    permission.ActionAllow,
		},
		{
			perm:    toolname.Write,
			pattern: ".chord/notes/findings-001.md",
			want:    permission.ActionAllow,
		},
		{perm: toolname.Write, pattern: "docs/plan.md", want: permission.ActionDeny},
		{
			perm:    toolname.Edit,
			pattern: ".chord/plans/plan-001.md",
			want:    permission.ActionAllow,
		},
		{
			perm:    toolname.Edit,
			pattern: ".chord/notes/findings-001.md",
			want:    permission.ActionAllow,
		},
		{perm: toolname.Edit, pattern: "docs/plan.md", want: permission.ActionDeny},
		{perm: toolname.Handoff, pattern: "*", want: permission.ActionAllow},
		{perm: toolname.CompactContext, pattern: "*", want: permission.ActionAllow},
	}
	for _, tt := range checks {
		if got := ruleset.Evaluate(tt.perm, tt.pattern); got != tt.want {
			t.Fatalf("planner %s permission for %q = %s, want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}

func TestDefaultPlannerAgentDescriptionMentionsPlanAndHandoff(t *testing.T) {
	cfg := DefaultPlannerAgent()
	for _, want := range []string{"creates a plan document", "calls Handoff"} {
		if !strings.Contains(cfg.Description, want) {
			t.Fatalf("planner description missing %q in %q", want, cfg.Description)
		}
	}
}

func TestDefaultBuilderAgentUsesAllowAllBaselineWithOverrides(t *testing.T) {
	cfg := DefaultBuilderAgent()
	ruleset := permission.ParsePermission(&cfg.Permission)
	checks := []struct {
		perm    string
		pattern string
		want    permission.Action
	}{
		{perm: toolname.Read, pattern: "internal/agent/main.go", want: permission.ActionAllow},
		{perm: toolname.Write, pattern: "docs/notes.md", want: permission.ActionAllow},
		{perm: toolname.Edit, pattern: "docs/notes.md", want: permission.ActionAllow},
		{perm: toolname.Shell, pattern: "go test ./...", want: permission.ActionAllow},
		{perm: toolname.Delete, pattern: "tmp/build.out", want: permission.ActionAsk},
		{perm: toolname.Delegate, pattern: "*", want: permission.ActionDeny},
		{perm: toolname.Handoff, pattern: "*", want: permission.ActionDeny},
	}
	for _, tt := range checks {
		if got := ruleset.Evaluate(tt.perm, tt.pattern); got != tt.want {
			t.Fatalf("builder %s permission for %q = %s, want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}

func TestDefaultPlannerAgentDeclaresPlanningPreset(t *testing.T) {
	cfg := DefaultPlannerAgent()
	if cfg.PromptPreset != PromptPresetPlanning {
		t.Fatalf("planner prompt_preset = %q, want %q", cfg.PromptPreset, PromptPresetPlanning)
	}
	if got := cfg.ResolvePromptPreset(); got != PromptPresetPlanning {
		t.Fatalf("planner ResolvePromptPreset() = %q, want %q", got, PromptPresetPlanning)
	}
}

func TestResolvePromptPreset(t *testing.T) {
	checks := []struct {
		name string
		cfg  *AgentConfig
		want string
	}{
		{name: "nil config", cfg: nil, want: ""},
		{
			name: "explicit preset on a custom name",
			cfg:  &AgentConfig{Name: "architect", PromptPreset: PromptPresetPlanning},
			want: PromptPresetPlanning,
		},
		{
			name: "explicit preset is case-insensitive",
			cfg:  &AgentConfig{Name: "architect", PromptPreset: "Planning"},
			want: PromptPresetPlanning,
		},
		{
			name: "none opts out even when the name would select a preset",
			cfg:  &AgentConfig{Name: "planner", PromptPreset: PromptPresetNone},
			want: "",
		},
		{
			name: "the name planner selects nothing on its own",
			cfg:  &AgentConfig{Name: "planner"},
			want: "",
		},
		{
			name: "unrelated name resolves to no preset",
			cfg:  &AgentConfig{Name: "reviewer"},
			want: "",
		},
	}
	for _, tt := range checks {
		if got := tt.cfg.ResolvePromptPreset(); got != tt.want {
			t.Fatalf("%s: ResolvePromptPreset() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestLoadAgentConfigRejectsUnknownPromptPreset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "architect.yaml")
	body := "name: architect\nmode: main\nprompt_preset: planing\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := LoadAgentConfig(path)
	if err == nil {
		t.Fatal("LoadAgentConfig() error = nil, want unknown prompt_preset error")
	}
	for _, want := range []string{"unknown prompt_preset", "planing", PromptPresetPlanning, PromptPresetNone} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

func TestLoadAgentConfigKeepsPromptPresetAndAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "architect.yaml")
	body := "name: architect\nmode: main\nprompt_preset: Planning\nprompt_append: |\n  Always cite the ADR number.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	// The loader canonicalises the preset so downstream comparisons need no
	// case folding of their own.
	if cfg.PromptPreset != PromptPresetPlanning {
		t.Fatalf("prompt_preset = %q, want %q", cfg.PromptPreset, PromptPresetPlanning)
	}
	if !strings.Contains(cfg.PromptAppend, "Always cite the ADR number.") {
		t.Fatalf("prompt_append = %q, want the appended guidance", cfg.PromptAppend)
	}
	if strings.TrimSpace(cfg.SystemPrompt) != "" {
		t.Fatalf("prompt_append must not populate SystemPrompt, got %q", cfg.SystemPrompt)
	}
}
