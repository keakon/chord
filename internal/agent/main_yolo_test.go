package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
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

func TestYoloBypassesOnlyUnprotectedToolPermissions(t *testing.T) {
	pipeline := toolExecutionPipeline{
		registry: tools.NewRegistry(),
		currentRuleset: func() permission.Ruleset {
			return permission.Ruleset{
				{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameHandoff, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionDeny},
				{Permission: tools.NameCancel, Pattern: "*", Action: permission.ActionDeny},
			}
		},
		bypassPermission: func(name string) bool {
			return !yoloProtectedPermissionTool(name)
		},
	}

	for _, toolName := range []string{tools.NameShell, tools.NameWrite, tools.NameRead} {
		t.Run(toolName+" bypassed", func(t *testing.T) {
			call := message.ToolCall{Name: toolName, Args: json.RawMessage(`{}`)}
			if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err != nil {
				t.Fatalf("applyPermission(%s) err = %v, want nil", toolName, err)
			}
		})
	}

	for _, toolName := range []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel} {
		t.Run(toolName+" protected", func(t *testing.T) {
			call := message.ToolCall{Name: toolName, Args: json.RawMessage(`{}`)}
			err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{})
			if err == nil || !strings.Contains(err.Error(), "denied by permission policy") {
				t.Fatalf("applyPermission(%s) err = %v, want permission denied", toolName, err)
			}
		})
	}
}
