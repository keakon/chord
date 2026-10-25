package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestRuleIntentPermissionsFallsBackToConfirmedTool(t *testing.T) {
	t.Parallel()
	rules := ruleIntentPermissions(tools.NameApplyPatch, &ConfirmRuleIntent{
		Rules: []ConfirmRule{
			{Tool: tools.NameDelete, Pattern: ".chord/notes/*"},
			{Pattern: "docs/*"},
			{Pattern: "   "},
		},
	})
	want := []permission.Rule{
		{Permission: tools.NameDelete, Pattern: ".chord/notes/*", Action: permission.ActionAllow},
		{Permission: tools.NameApplyPatch, Pattern: "docs/*", Action: permission.ActionAllow},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %#v, want %#v", rules, want)
	}
}

func TestRuleIntentPermissionsNilIntent(t *testing.T) {
	t.Parallel()
	if rules := ruleIntentPermissions(tools.NameShell, nil); rules != nil {
		t.Fatalf("rules = %#v, want nil", rules)
	}
}

// An apply_patch confirmation blocked by a delete rule must install the rule
// under delete, not apply_patch: only a delete allow lifts the delete ask on a
// moved source path. The session overlay outranks the base rules on the next
// evaluation.
func TestMainAgentRuleIntentInstallsPerToolRule(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.ruleset = permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionAllow},
		{Permission: "delete", Pattern: "*", Action: permission.ActionAsk},
	}

	a.processRuleIntent(tools.NameApplyPatch, &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Tool: tools.NameDelete, Pattern: ".chord/notes/*"}},
		Scope: int(permission.ScopeSession),
	}, a.currentAgentName())

	if got := a.effectiveRuleset().Evaluate(tools.NameDelete, ".chord/notes/a.md"); got != permission.ActionAllow {
		t.Fatalf("delete permission after rule intent = %q, want allow", got)
	}
	bucket := a.overlay.SessionRulesForRole(a.currentAgentName())
	want := permission.Ruleset{{Permission: tools.NameDelete, Pattern: ".chord/notes/*", Action: permission.ActionAllow}}
	if !reflect.DeepEqual(bucket, want) {
		t.Fatalf("session bucket = %#v, want %#v", bucket, want)
	}
}

// The reported symptom: a patch moving a file out of .chord asks because of the
// delete rule, the user remembers a rule for the current session, and the next
// identical patch asks again. Only a delete rule lifts that ask, so an intent
// naming the confirmed tool leaves the prompt in place.
func TestRuleIntentDeleteRuleStopsRepeatedApplyPatchAsk(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.activeConfig = &config.AgentConfig{
		Name: "builder",
		Permission: parsePermissionNode(t, `
"*": allow
apply_patch: allow
delete:
  ".chord/*": ask
`),
	}
	a.initOverlay()

	args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: .chord/notes/a.md\n*** Move to: .chord/notes/archive/a.md\n@@\n-old\n+new\n*** End Patch"}`)
	evaluate := func() toolPermissionDecision {
		return evaluateToolPermissionInDir(a.effectiveRuleset(), tools.NameApplyPatch, args, permission.PathScope{})
	}

	if got := evaluate(); got.Action != permission.ActionAsk {
		t.Fatalf("initial action = %q, want %q", got.Action, permission.ActionAsk)
	}

	a.processRuleIntent(tools.NameApplyPatch, &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Tool: tools.NameApplyPatch, Pattern: ".chord/notes/*"}},
		Scope: int(permission.ScopeSession),
	}, "builder")
	if got := evaluate(); got.Action != permission.ActionAsk {
		t.Fatalf("action after apply_patch rule = %q, want %q (delete ask still blocks)", got.Action, permission.ActionAsk)
	}

	a.processRuleIntent(tools.NameApplyPatch, &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Tool: tools.NameDelete, Pattern: ".chord/notes/*"}},
		Scope: int(permission.ScopeSession),
	}, "builder")
	if got := evaluate(); got.Action != permission.ActionAllow {
		t.Fatalf("action after delete rule = %q, want %q", got.Action, permission.ActionAllow)
	}
}
