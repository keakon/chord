package agent

import (
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestYoloRulesetKeepsProtectedRulesAndDropsOthers(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameHandoff, Pattern: "*", Action: permission.ActionAllow},
		{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionAsk},
		{Permission: tools.NameCancel, Pattern: "*", Action: permission.ActionDeny},
	}
	filtered := yoloRuleset(ruleset)

	for _, rule := range filtered {
		if !yoloProtectedPermissionTool(rule.Permission) {
			t.Fatalf("non-protected rule %+v should be filtered in YOLO ruleset: %+v", rule, filtered)
		}
	}

	// Default deny: Shell allow rule was filtered out, so Shell evaluates to deny.
	if got := evaluateToolPermission(filtered, tools.NameShell, json.RawMessage(`{"command":"rm -rf /"}`)); got.Action != permission.ActionDeny {
		t.Fatalf("Shell action under YOLO = %v, want deny (allow rule should be filtered)", got.Action)
	}
	// Protected rules survive with their original action (Handoff allow, Delegate ask, Cancel deny).
	if got := evaluateToolPermission(filtered, tools.NameHandoff, json.RawMessage(`{"agent":"planner"}`)); got.Action != permission.ActionAllow {
		t.Fatalf("Handoff action = %v, want allow", got.Action)
	}
	if got := evaluateToolPermission(filtered, tools.NameDelegate, json.RawMessage(`{"agent":"builder"}`)); got.Action != permission.ActionAsk {
		t.Fatalf("Delegate action = %v, want ask", got.Action)
	}
	if got := evaluateToolPermission(filtered, tools.NameCancel, json.RawMessage(`{}`)); got.Action != permission.ActionDeny {
		t.Fatalf("Cancel action = %v, want deny", got.Action)
	}
}

func TestYoloRulesetEmptyInputReturnsNil(t *testing.T) {
	if got := yoloRuleset(nil); got != nil {
		t.Fatalf("yoloRuleset(nil) = %v, want nil", got)
	}
	if got := yoloRuleset(permission.Ruleset{}); got != nil {
		t.Fatalf("yoloRuleset(empty) = %v, want nil", got)
	}
}
