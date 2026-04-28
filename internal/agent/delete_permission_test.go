package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestMainLLMToolDefinitionsHonorsDeletePermission(t *testing.T) {
	a := &MainAgent{tools: tools.NewRegistry()}
	a.tools.Register(tools.ReadTool{})
	a.tools.Register(tools.DeleteTool{})
	a.activeConfig = &config.AgentConfig{Permission: parsePermissionNode(t, `
"*": deny
Read: allow
Delete: deny
`)}
	a.rebuildRuleset()

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("tool def count = %d, want 1", len(defs))
	}
	if defs[0].Name != "Read" {
		t.Fatalf("visible tool = %q, want Read", defs[0].Name)
	}
}

func TestEvaluateToolPermissionDeleteAggregatesAskAndAllow(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Delete:
  "gen/*": allow
  "tmp/*": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := mustDeletePermissionArgs(t, []string{"tmp/build.out", "gen/client_old.go"})

	got := evaluateToolPermission(ruleset, "Delete", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if got.MatchArgument != "tmp/build.out" {
		t.Fatalf("match argument = %q, want tmp/build.out", got.MatchArgument)
	}
	if want := []string{"tmp/build.out"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
		t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
	}
	if want := []string{"gen/client_old.go"}; !reflect.DeepEqual(got.AlreadyAllowedPaths, want) {
		t.Fatalf("already allowed = %#v, want %#v", got.AlreadyAllowedPaths, want)
	}
}

func TestEvaluateToolPermissionDeleteDenyWins(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Delete:
  "gen/*": allow
  "tmp/*": ask
  "secret/*": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustDeletePermissionArgs(t, []string{"gen/client_old.go", "secret/plan.txt", "tmp/build.out"})

	got := evaluateToolPermission(ruleset, "Delete", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
	if got.MatchArgument != "secret/plan.txt" {
		t.Fatalf("match argument = %q, want secret/plan.txt", got.MatchArgument)
	}
	if len(got.NeedsApprovalPaths) != 0 || len(got.AlreadyAllowedPaths) != 0 {
		t.Fatalf("expected deny to clear grouped paths, got ask=%#v allow=%#v", got.NeedsApprovalPaths, got.AlreadyAllowedPaths)
	}
}

func TestEvaluateToolPermissionBashDenyWinsForCompoundCommand(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
  "rm *": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `cd build && rm out.txt`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
	if got.MatchArgument != "rm out.txt" {
		t.Fatalf("match argument = %q, want rm out.txt", got.MatchArgument)
	}
}

func TestEvaluateToolPermissionBashAskWhenAnySubcommandNeedsApproval(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
  "git *": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `git status && pwd`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if got.MatchArgument != "pwd" {
		t.Fatalf("match argument = %q, want pwd", got.MatchArgument)
	}
	if want := []string{"pwd"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
		t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
	}
	if want := []string{"git status"}; !reflect.DeepEqual(got.AlreadyAllowedPaths, want) {
		t.Fatalf("already allowed = %#v, want %#v", got.AlreadyAllowedPaths, want)
	}
}

func TestEvaluateToolPermissionBashAllowsWhenAllSubcommandsAllowed(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
  "git *": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `git status && git diff --stat`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionAllow {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAllow)
	}
	if got.MatchArgument != "git status" {
		t.Fatalf("match argument = %q, want git status", got.MatchArgument)
	}
	if len(got.NeedsApprovalPaths) != 0 {
		t.Fatalf("needs approval = %#v, want empty", got.NeedsApprovalPaths)
	}
	if want := []string{"git status", "git diff --stat"}; !reflect.DeepEqual(got.AlreadyAllowedPaths, want) {
		t.Fatalf("already allowed = %#v, want %#v", got.AlreadyAllowedPaths, want)
	}
}

func TestEvaluateToolPermissionBashExactWholeCommandRuleOverridesSubcommandDeny(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
  "rm *": deny
  "cd build && rm out.txt": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `cd build && rm out.txt`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionAllow {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAllow)
	}
	if got.MatchArgument != "cd build && rm out.txt" {
		t.Fatalf("match argument = %q, want full command", got.MatchArgument)
	}
}

func TestEvaluateToolPermissionBashStripsLeadingAssignmentsForSubcommandMatch(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
  "rm *": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `FOO=bar rm out.txt`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
	if got.MatchArgument != "rm out.txt" {
		t.Fatalf("match argument = %q, want rm out.txt", got.MatchArgument)
	}
}

func TestEvaluateToolPermissionBashFallsBackToWholeCommandOnParseError(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
  "echo hi &&": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `echo hi &&`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
	if got.MatchArgument != "echo hi &&" {
		t.Fatalf("match argument = %q, want full command", got.MatchArgument)
	}
	if len(got.NeedsApprovalPaths) != 0 || len(got.AlreadyAllowedPaths) != 0 {
		t.Fatalf("expected deny to clear grouped paths, got ask=%#v allow=%#v", got.NeedsApprovalPaths, got.AlreadyAllowedPaths)
	}
}

func TestEvaluateToolPermissionBashParseErrorAskIncludesNeedsApproval(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Bash:
  "*": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `echo hi &&`)

	got := evaluateToolPermission(ruleset, "Bash", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if want := []string{"echo hi &&"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
		t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
	}
}

func TestEvaluateToolPermissionCancelDeniedWhenDelegateDisabled(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
Delegate: deny
Cancel: allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustCancelPermissionArgs(t, "adhoc-1")

	got := evaluateToolPermission(ruleset, "Cancel", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
}

func mustDeletePermissionArgs(t *testing.T, paths []string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"paths":  paths,
		"reason": "cleanup",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return message.ToolCall{Args: b}.Args
}

func mustBashPermissionArgs(t *testing.T, command string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"command": command,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return message.ToolCall{Args: b}.Args
}

func mustCancelPermissionArgs(t *testing.T, taskID string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"target_task_id": taskID,
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return message.ToolCall{Args: b}.Args
}
