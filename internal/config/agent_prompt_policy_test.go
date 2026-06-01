package config

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func TestDefaultPlannerAgentUsesUpdatedPermissionPolicy(t *testing.T) {
	cfg := DefaultPlannerAgent()
	ruleset := permission.ParsePermission(&cfg.Permission)
	checks := []struct {
		perm    string
		pattern string
		want    permission.Action
	}{
		{perm: "Shell", pattern: "go test ./...", want: permission.ActionAllow},
		{perm: "Read", pattern: "internal/agent/main.go", want: permission.ActionAllow},
		{
			perm:    "Write",
			pattern: ".chord/plans/plan-001.md",
			want:    permission.ActionAllow,
		},
		{perm: "Write", pattern: "docs/plan.md", want: permission.ActionDeny},
		{
			perm:    "Edit",
			pattern: ".chord/plans/plan-001.md",
			want:    permission.ActionAllow,
		},
		{perm: "Edit", pattern: "docs/plan.md", want: permission.ActionDeny},
		{perm: "Handoff", pattern: "*", want: permission.ActionAllow},
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
		{perm: "Read", pattern: "internal/agent/main.go", want: permission.ActionAllow},
		{perm: "Write", pattern: "docs/notes.md", want: permission.ActionAllow},
		{perm: "Edit", pattern: "docs/notes.md", want: permission.ActionAllow},
		{perm: "Shell", pattern: "go test ./...", want: permission.ActionAllow},
		{perm: "Delete", pattern: "tmp/build.out", want: permission.ActionAsk},
		{perm: "Delegate", pattern: "*", want: permission.ActionDeny},
		{perm: "Handoff", pattern: "*", want: permission.ActionDeny},
	}
	for _, tt := range checks {
		if got := ruleset.Evaluate(tt.perm, tt.pattern); got != tt.want {
			t.Fatalf("builder %s permission for %q = %s, want %s", tt.perm, tt.pattern, got, tt.want)
		}
	}
}
