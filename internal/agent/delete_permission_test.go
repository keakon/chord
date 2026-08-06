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
read: allow
delete: deny
`)}
	a.rebuildRuleset()

	defs := a.mainLLMToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("tool def count = %d, want 1", len(defs))
	}
	if defs[0].Name != "read" {
		t.Fatalf("visible tool = %q, want read", defs[0].Name)
	}
}

func TestEvaluateToolPermissionDeleteAggregatesAskAndAllow(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
delete:
  "gen/*": allow
  "tmp/*": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := mustDeletePermissionArgs(t, []string{"tmp/build.out", "gen/client_old.go"})

	got := evaluateToolPermission(ruleset, "delete", args)
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
delete:
  "gen/*": allow
  "tmp/*": ask
  "secret/*": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustDeletePermissionArgs(t, []string{"gen/client_old.go", "secret/plan.txt", "tmp/build.out"})

	got := evaluateToolPermission(ruleset, "delete", args)
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

func TestEvaluateToolPermissionApplyPatchChecksEveryTarget(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
apply_patch:
  "allowed/*": allow
  "secret/*": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: allowed/ok.txt\n+ok\n*** Add File: secret/no.txt\n+no\n*** End Patch"}`)

	got := evaluateToolPermission(ruleset, tools.NameApplyPatch, args)
	if got.Action != permission.ActionDeny || got.MatchArgument != "secret/no.txt" {
		t.Fatalf("decision = %#v, want secret target denial", got)
	}
}

// Tool-specific delete rules must constrain `*** Delete File:` operations inside
// apply_patch: when the delete tool is hidden for patch-native models, deletion
// must not silently drop to the (typically laxer) apply_patch threshold.
func TestEvaluateToolPermissionApplyPatchLayersDeleteRules(t *testing.T) {
	deletePatch := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: src/main.go\n@@\n-old\n+new\n*** Delete File: tmp/obsolete.txt\n*** End Patch"}`)

	t.Run("delete ask escalates only the delete target", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": deny
apply_patch: allow
delete: ask
`)
		ruleset := permission.ParsePermission(&node)
		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, deletePatch)
		if got.Action != permission.ActionAsk {
			t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
		}
		if got.MatchArgument != "tmp/obsolete.txt" {
			t.Fatalf("match argument = %q, want tmp/obsolete.txt", got.MatchArgument)
		}
		if want := []string{"tmp/obsolete.txt"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
			t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
		}
		if want := []string{"src/main.go"}; !reflect.DeepEqual(got.AlreadyAllowedPaths, want) {
			t.Fatalf("already allowed = %#v, want %#v", got.AlreadyAllowedPaths, want)
		}
	})

	t.Run("scoped delete deny blocks the patch", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": deny
apply_patch: allow
delete:
  "tmp/*": deny
`)
		ruleset := permission.ParsePermission(&node)
		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, deletePatch)
		if got.Action != permission.ActionDeny || got.MatchArgument != "tmp/obsolete.txt" {
			t.Fatalf("decision = %#v, want delete-rule denial of tmp/obsolete.txt", got)
		}
	})

	t.Run("wildcard-only rules are not layered", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": allow
`)
		ruleset := permission.ParsePermission(&node)
		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, deletePatch)
		if got.Action != permission.ActionAllow {
			t.Fatalf("action = %q, want %q (wildcard allow must not be escalated)", got.Action, permission.ActionAllow)
		}
	})

	t.Run("delete allow cannot lower the apply_patch threshold", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": deny
apply_patch: ask
delete: allow
`)
		ruleset := permission.ParsePermission(&node)
		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, deletePatch)
		if got.Action != permission.ActionAsk {
			t.Fatalf("action = %q, want %q (stricter action wins)", got.Action, permission.ActionAsk)
		}
	})
}

func TestEvaluateToolPermissionApplyPatchLayersHiddenOperationRules(t *testing.T) {
	t.Run("add inherits explicit write threshold", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": allow
write:
  "generated/*": ask
`)
		ruleset := permission.ParsePermission(&node)
		args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: generated/client.go\n+package generated\n*** End Patch"}`)

		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, args)
		if got.Action != permission.ActionAsk || got.MatchArgument != "generated/client.go" {
			t.Fatalf("decision = %#v, want write-rule approval", got)
		}
	})

	t.Run("move inherits delete rule on source", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": allow
delete:
  "protected/*": deny
`)
		ruleset := permission.ParsePermission(&node)
		args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: protected/key.txt\n*** Move to: archive/key.txt\n@@\n-secret\n+archived\n*** End Patch"}`)

		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, args)
		if got.Action != permission.ActionDeny || got.MatchArgument != "protected/key.txt" {
			t.Fatalf("decision = %#v, want delete-rule denial of move source", got)
		}
	})

	t.Run("move inherits write rule on target", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": allow
write:
  "protected/*": ask
`)
		ruleset := permission.ParsePermission(&node)
		args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: src/key.txt\n*** Move to: protected/key.txt\n@@\n-secret\n+archived\n*** End Patch"}`)

		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, args)
		if got.Action != permission.ActionAsk || got.MatchArgument != "protected/key.txt" {
			t.Fatalf("decision = %#v, want write-rule approval for move target", got)
		}
	})

	t.Run("move then recreate source layers all path operations", func(t *testing.T) {
		node := parsePermissionNode(t, `
"*": allow
write:
  "plans/current.md": ask
  "plans/archive/*": ask
delete:
  "plans/current.md": ask
`)
		ruleset := permission.ParsePermission(&node)
		args := json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: plans/current.md\n*** Move to: plans/archive/old.md\n@@\n-old\n+archived\n*** Add File: plans/current.md\n+new\n*** End Patch"}`)

		got := evaluateToolPermission(ruleset, tools.NameApplyPatch, args)
		if got.Action != permission.ActionAsk {
			t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
		}
		if want := []string{"plans/current.md", "plans/archive/old.md"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
			t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
		}
	})
}

func TestEvaluateToolPermissionGlobDenyWinsAcrossPatterns(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
glob:
  "allowed/**": allow
  "secret/**": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := json.RawMessage(`{"patterns":["allowed/**","secret/**"]}`)

	got := evaluateToolPermission(ruleset, "glob", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
	if got.MatchArgument != "secret/**" {
		t.Fatalf("match argument = %q, want secret/**", got.MatchArgument)
	}
}

func TestEvaluateToolPermissionGlobAskWinsOverAllowedPattern(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
glob:
  "allowed/**": allow
  "ask/**": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := json.RawMessage(`{"patterns":["allowed/**","ask/**"]}`)

	got := evaluateToolPermission(ruleset, "glob", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if got.MatchArgument != "ask/**" {
		t.Fatalf("match argument = %q, want ask/**", got.MatchArgument)
	}
	if want := []string{"ask/**"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
		t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
	}
}

func TestEvaluateToolPermissionGlobAllPatternsAllow(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
glob:
  "**/*.go": allow
  "**/*.md": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := json.RawMessage(`{"patterns":["**/*.go","**/*.md"]}`)

	got := evaluateToolPermission(ruleset, "glob", args)
	if got.Action != permission.ActionAllow {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAllow)
	}
}

func TestEvaluateToolPermissionGlobScalarPatternMatchesArrayDeny(t *testing.T) {
	node := parsePermissionNode(t, `
"*": allow
glob:
  "secret/**": deny
`)
	ruleset := permission.ParsePermission(&node)

	arrayArgs := json.RawMessage(`{"patterns":["secret/**"]}`)
	scalarArgs := json.RawMessage(`{"patterns":"secret/**"}`)

	arrayGot := evaluateToolPermission(ruleset, "glob", arrayArgs)
	scalarGot := evaluateToolPermission(ruleset, "glob", scalarArgs)

	if arrayGot.Action != permission.ActionDeny {
		t.Fatalf("array action = %q, want %q", arrayGot.Action, permission.ActionDeny)
	}
	// A single bare-string pattern must not bypass a deny rule that the array
	// form would have matched.
	if scalarGot.Action != arrayGot.Action {
		t.Fatalf("scalar action = %q, want %q (parity with array form)", scalarGot.Action, arrayGot.Action)
	}
	if scalarGot.MatchArgument != "secret/**" {
		t.Fatalf("scalar match argument = %q, want secret/**", scalarGot.MatchArgument)
	}
}

func TestEvaluateToolPermissionBashDenyWinsForCompoundCommand(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
shell:
  "*": ask
  "rm *": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `cd build && rm out.txt`)

	got := evaluateToolPermission(ruleset, "shell", args)
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
shell:
  "*": ask
  "git *": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `git status && pwd`)

	got := evaluateToolPermission(ruleset, "shell", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if got.MatchArgument != "pwd" {
		t.Fatalf("match argument = %q, want pwd", got.MatchArgument)
	}
	if want := []string{"pwd"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
		t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
	}
	if want := []string{"*"}; !reflect.DeepEqual(got.NeedsApprovalRules, want) {
		t.Fatalf("needs approval rules = %#v, want %#v", got.NeedsApprovalRules, want)
	}
	if want := []string{"git status"}; !reflect.DeepEqual(got.AlreadyAllowedPaths, want) {
		t.Fatalf("already allowed = %#v, want %#v", got.AlreadyAllowedPaths, want)
	}
	if want := []string{"git *"}; !reflect.DeepEqual(got.AlreadyAllowedRules, want) {
		t.Fatalf("already allowed rules = %#v, want %#v", got.AlreadyAllowedRules, want)
	}
}

func TestEvaluateToolPermissionBashReportsMultipleAskRuleMatches(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
shell:
  "git reset *": ask
  "git add *": ask
  "git commit *": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `git reset HEAD^ && git add CHANGELOG.md && git commit -m fix`)

	got := evaluateToolPermission(ruleset, "shell", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if want := []string{"git reset HEAD^", "git add CHANGELOG.md", "git commit -m fix"}; !reflect.DeepEqual(got.NeedsApprovalPaths, want) {
		t.Fatalf("needs approval = %#v, want %#v", got.NeedsApprovalPaths, want)
	}
	if want := []string{"git reset *", "git add *", "git commit *"}; !reflect.DeepEqual(got.NeedsApprovalRules, want) {
		t.Fatalf("needs approval rules = %#v, want %#v", got.NeedsApprovalRules, want)
	}
}

func TestEvaluateToolPermissionBashAllowsWhenAllSubcommandsAllowed(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
shell:
  "*": ask
  "git *": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `git status && git diff --stat`)

	got := evaluateToolPermission(ruleset, "shell", args)
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
shell:
  "*": ask
  "rm *": deny
  "cd build && rm out.txt": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `cd build && rm out.txt`)

	got := evaluateToolPermission(ruleset, "shell", args)
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
shell:
  "*": ask
  "rm *": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `FOO=bar rm out.txt`)

	got := evaluateToolPermission(ruleset, "shell", args)
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
shell:
  "*": ask
  "echo hi &&": deny
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `echo hi &&`)

	got := evaluateToolPermission(ruleset, "shell", args)
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

func TestEvaluateToolPermissionBashParseErrorDoesNotAllowCompoundCommandByNarrowRule(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
shell:
  "*": ask
  "git *": allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `git status; "`)

	got := evaluateToolPermission(ruleset, "shell", args)
	if got.Action != permission.ActionAsk {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAsk)
	}
	if got.MatchArgument != `git status; "` {
		t.Fatalf("match argument = %q, want full command", got.MatchArgument)
	}
	if want := []string{"*"}; !reflect.DeepEqual(got.NeedsApprovalRules, want) {
		t.Fatalf("needs approval rules = %#v, want %#v", got.NeedsApprovalRules, want)
	}
	if len(got.AlreadyAllowedPaths) != 0 || len(got.AlreadyAllowedRules) != 0 {
		t.Fatalf("already allowed = %#v/%#v, want empty", got.AlreadyAllowedPaths, got.AlreadyAllowedRules)
	}
}

func TestEvaluateToolPermissionBashParseErrorAskIncludesNeedsApproval(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
shell:
  "*": ask
`)
	ruleset := permission.ParsePermission(&node)
	args := mustBashPermissionArgs(t, `echo hi &&`)

	got := evaluateToolPermission(ruleset, "shell", args)
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
delegate: deny
cancel: allow
`)
	ruleset := permission.ParsePermission(&node)
	args := mustCancelPermissionArgs(t, "adhoc-1")

	got := evaluateToolPermission(ruleset, "cancel", args)
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
}

func TestEvaluateToolPermissionWebFetchMatchesURL(t *testing.T) {
	node := parsePermissionNode(t, `
"*": allow
web_fetch:
  "localhost:8000": deny
`)
	ruleset := permission.ParsePermission(&node)

	got := evaluateToolPermission(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, "http://localhost:8000/docs/index.html"))
	if got.Action != permission.ActionDeny {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionDeny)
	}
	if got.MatchArgument != "http://localhost:8000/docs/index.html" {
		t.Fatalf("match argument = %q", got.MatchArgument)
	}

	got = evaluateToolPermission(ruleset, "web_fetch", mustWebFetchPermissionArgs(t, "http://localhost:9000/docs/index.html"))
	if got.Action != permission.ActionAllow {
		t.Fatalf("action = %q, want %q", got.Action, permission.ActionAllow)
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

func mustWebFetchPermissionArgs(t *testing.T, url string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"url": url,
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

// TestEvaluateToolPermissionApplyPatchCleansTargetPaths pins deny rules
// against path-spelling dodges: "./secret/x" and "allowed/../secret/x" must
// hit a "secret/*" deny exactly like the plainly spelled path.
func TestEvaluateToolPermissionApplyPatchCleansTargetPaths(t *testing.T) {
	node := parsePermissionNode(t, `
"*": deny
apply_patch:
  "allowed/*": allow
  "secret/*": deny
`)
	ruleset := permission.ParsePermission(&node)
	for name, patch := range map[string]string{
		"dot-slash":  "*** Begin Patch\n*** Add File: ./secret/no.txt\n+no\n*** End Patch",
		"parent-hop": "*** Begin Patch\n*** Add File: allowed/../secret/no.txt\n+no\n*** End Patch",
	} {
		t.Run(name, func(t *testing.T) {
			args, err := json.Marshal(map[string]string{"patch": patch})
			if err != nil {
				t.Fatal(err)
			}
			got := evaluateToolPermission(ruleset, tools.NameApplyPatch, args)
			if got.Action != permission.ActionDeny || got.MatchArgument != "secret/no.txt" {
				t.Fatalf("decision = %#v, want cleaned secret target denial", got)
			}
		})
	}
}
