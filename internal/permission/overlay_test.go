package permission

import (
	"testing"
	"time"
)

func TestOverlay_AddAndRemove(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("test-role")
	o.SetBase(Ruleset{
		{Permission: "Shell", Pattern: "*", Action: ActionAsk},
	})

	rule := Rule{Permission: "Shell", Pattern: "git log *", Action: ActionAllow}
	o.AddSessionRule("test-role", rule)

	// Check merged ruleset
	merged := o.MergedRuleset()
	result := merged.Evaluate("Shell", "git log --oneline")
	if result != ActionAllow {
		t.Errorf("expected ActionAllow after adding session rule, got %v", result)
	}

	// Remove it
	o.RemoveSessionRule(0)
	merged = o.MergedRuleset()
	result = merged.Evaluate("Shell", "git log --oneline")
	if result != ActionAsk {
		t.Errorf("expected ActionAsk after removing session rule, got %v", result)
	}
}

func TestOverlay_AddedRules(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	o.SetBase(Ruleset{
		{Permission: "Shell", Pattern: "*", Action: ActionAsk},
	})

	rule := Rule{Permission: "Shell", Pattern: "git *", Action: ActionAllow}
	o.AddSessionRule("builder", rule)

	added := o.AddedRules()
	if len(added) != 1 {
		t.Fatalf("expected 1 added rule, got %d", len(added))
	}
	if added[0].Role != "builder" {
		t.Errorf("expected role 'builder', got %q", added[0].Role)
	}
	if added[0].Scope != ScopeSession {
		t.Errorf("expected ScopeSession, got %v", added[0].Scope)
	}
}

func TestOverlay_AddSessionRule_Deduplicates(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	rule := Rule{Permission: "Shell", Pattern: "git *", Action: ActionAllow}
	o.AddSessionRule("builder", rule)
	o.AddSessionRule("builder", rule)
	if got := len(o.AddedRules()); got != 1 {
		t.Fatalf("added rules = %d, want 1", got)
	}
	if got := o.SessionRuleCount(); got != 1 {
		t.Fatalf("session rules = %d, want 1", got)
	}
}

func TestOverlay_AddPersistentRule_RejectsSessionScope(t *testing.T) {
	o := NewOverlay()
	rule := Rule{Permission: "Shell", Pattern: "git *", Action: ActionAllow}
	if err := o.AddPersistentRule("builder", rule, ScopeSession, ""); err == nil {
		t.Fatal("expected AddPersistentRule to fail for session scope")
	}
}

func TestOverlay_AddPersistentRuleTracksPathAndMerges(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	o.SetBase(Ruleset{{Permission: "Shell", Pattern: "*", Action: ActionAsk}})
	rule := Rule{Permission: "Shell", Pattern: "git *", Action: ActionAllow}
	path := "/tmp/chord-test-agent.yaml"

	if err := o.AddPersistentRule("builder", rule, ScopeProject, path); err != nil {
		t.Fatalf("AddPersistentRule failed: %v", err)
	}
	if got := o.MergedRuleset().Evaluate("Shell", "git status"); got != ActionAllow {
		t.Fatalf("merged evaluation = %s, want %s", got, ActionAllow)
	}
	added := o.AddedRules()
	if len(added) != 1 {
		t.Fatalf("added rules = %d, want 1", len(added))
	}
	if added[0].Path != path {
		t.Fatalf("added path = %q, want %q", added[0].Path, path)
	}
}

func TestOverlay_SessionRulesAreRoleScoped(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	o.SetBase(Ruleset{{Permission: "Shell", Pattern: "*", Action: ActionAsk}})
	builderRule := Rule{Permission: "Shell", Pattern: "git *", Action: ActionAllow}
	o.AddSessionRule("builder", builderRule)

	if got := o.MergedRuleset().Evaluate("Shell", "git status"); got != ActionAllow {
		t.Fatalf("builder merged evaluation = %s, want %s", got, ActionAllow)
	}
	if got := o.SessionRuleCountForRole("builder"); got != 1 {
		t.Fatalf("builder session count = %d, want 1", got)
	}

	o.SetActiveRole("planner")
	if got := o.MergedRuleset().Evaluate("Shell", "git status"); got != ActionAsk {
		t.Fatalf("planner merged evaluation = %s, want %s", got, ActionAsk)
	}
	if got := o.SessionRuleCount(); got != 0 {
		t.Fatalf("active planner session count = %d, want 0", got)
	}
}

func TestOverlay_AddedRulesPreserveRemovalIndexOrder(t *testing.T) {
	o := NewOverlay()
	o.SetActiveRole("builder")
	older := Rule{Permission: "Shell", Pattern: "git log *", Action: ActionAllow}
	newer := Rule{Permission: "Shell", Pattern: "git status *", Action: ActionAllow}
	o.AddSessionRule("builder", older)
	o.AddSessionRule("builder", newer)

	// Simulate non-monotonic timestamps from restore/manual construction. The UI
	// must see insertion order because RemoveAddedRule accepts the displayed index.
	o.addedRules[0].AddedAt = time.Now().Add(time.Hour)
	o.addedRules[1].AddedAt = time.Now().Add(-time.Hour)

	added := o.AddedRules()
	if got := added[0].Rule.Pattern; got != "git log *" {
		t.Fatalf("first displayed rule = %q, want insertion-order git log *", got)
	}
	if err := o.RemoveAddedRule(0); err != nil {
		t.Fatalf("RemoveAddedRule failed: %v", err)
	}
	remaining := o.AddedRules()
	if got := len(remaining); got != 1 {
		t.Fatalf("remaining added rules = %d, want 1", got)
	}
	if got := remaining[0].Rule.Pattern; got != "git status *" {
		t.Fatalf("remaining rule = %q, want git status *", got)
	}
}
