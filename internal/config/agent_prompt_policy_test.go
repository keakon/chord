package config

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func TestDefaultPlannerAgentUsesConservativeBashPermission(t *testing.T) {
	cfg := DefaultPlannerAgent()
	ruleset := permission.ParsePermission(&cfg.Permission)
	if got := ruleset.Evaluate("Bash", "go test ./..."); got != permission.ActionAsk {
		t.Fatalf("planner Bash permission = %s, want ask", got)
	}
	if got := ruleset.Evaluate("Read", "internal/agent/main.go"); got != permission.ActionAllow {
		t.Fatalf("planner Read permission = %s, want allow", got)
	}
	if got := ruleset.Evaluate("Handoff", "*"); got != permission.ActionAllow {
		t.Fatalf("planner Handoff permission = %s, want allow", got)
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
