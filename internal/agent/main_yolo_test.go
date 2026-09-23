package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
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
		{Permission: tools.NameDone, Pattern: "*", Action: permission.ActionAllow},
	}
	filtered := yoloRuleset(ruleset)

	for _, rule := range filtered {
		if !yoloProtectedPermissionTool(rule.Permission) {
			t.Fatalf("non-protected rule %+v should be filtered in YOLO ruleset: %+v", rule, filtered)
		}
	}

	// The Shell allow rule is filtered out of the YOLO decision surface
	// entirely: ordinary tools never evaluate against it (the execution gate
	// bypasses them under YOLO), so an unfiltered leftover here would deny
	// nothing but would contradict the visible bypass. At this unit level the
	// filtered ruleset therefore has no Shell rule at all.
	for _, rule := range filtered {
		if rule.Permission == tools.NameShell {
			t.Fatalf("Shell rule %+v must be filtered from the YOLO ruleset", rule)
		}
	}
	// Protected rules survive with their original action (Handoff allow, Delegate ask, Cancel deny, Done allow).
	if got := evaluateToolPermissionInDir(filtered, tools.NameHandoff, json.RawMessage(`{"agent":"planner"}`), permission.PathScope{}); got.Action != permission.ActionAllow {
		t.Fatalf("Handoff action = %v, want allow", got.Action)
	}
	if got := evaluateToolPermissionInDir(filtered, tools.NameDelegate, json.RawMessage(`{"agent_type":"builder"}`), permission.PathScope{}); got.Action != permission.ActionAsk {
		t.Fatalf("Delegate action = %v, want ask", got.Action)
	}
	if got := evaluateToolPermissionInDir(filtered, tools.NameCancel, json.RawMessage(`{}`), permission.PathScope{}); got.Action != permission.ActionDeny {
		t.Fatalf("Cancel action = %v, want deny", got.Action)
	}
	if got := evaluateToolPermissionInDir(filtered, tools.NameDone, json.RawMessage(`{"report":"done"}`), permission.PathScope{}); got.Action != permission.ActionAllow {
		t.Fatalf("Done action = %v, want allow", got.Action)
	}
}

func TestEvaluateDelegatePermissionMatchesAgentType(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: tools.NameDelegate, Pattern: "*", Action: permission.ActionDeny},
		{Permission: tools.NameDelegate, Pattern: "reviewer", Action: permission.ActionAllow},
		{Permission: tools.NameDelegate, Pattern: "tester", Action: permission.ActionAsk},
	}

	for _, tc := range []struct {
		agentType string
		want      permission.Action
	}{
		{agentType: "reviewer", want: permission.ActionAllow},
		{agentType: "tester", want: permission.ActionAsk},
		{agentType: "builder", want: permission.ActionDeny},
	} {
		args := json.RawMessage(`{"agent_type":"` + tc.agentType + `"}`)
		got := evaluateToolPermissionInDir(ruleset, tools.NameDelegate, args, permission.PathScope{})
		if got.Action != tc.want {
			t.Errorf("Delegate(%q) action = %v, want %v", tc.agentType, got.Action, tc.want)
		}
		if got.MatchArgument != tc.agentType {
			t.Errorf("Delegate(%q) match argument = %q", tc.agentType, got.MatchArgument)
		}
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

// TestYoloRulesetMechanismDefaultsMatchFullRuleset pins the core invariant:
// the YOLO ruleset must decide delegate/handoff/cancel exactly as the user's
// full ruleset does, because YOLO may only widen permissions, never narrow
// them. Wildcard defaults are mirrored onto the control tools, and a ruleset
// with no wildcard gets the engine's no-match default (deny) mirrored for
// tools the user did not mention.
func TestYoloRulesetMechanismDefaultsMatchFullRuleset(t *testing.T) {
	configs := []string{
		`"*": allow`,
		`"*": deny`,
		`read: allow`,
		`"*": deny
delegate: allow`,
		`delegate: deny`,
		`"*": allow
delegate: deny`,
		`"*": deny
handoff: ask`,
		`"*": deny
delegate:
  reviewer: allow
  tester: ask`,
	}
	// Cancel and handoff calls always evaluate with a "*" argument; delegate
	// matches on agent_type, so sample a few types.
	for _, src := range configs {
		full := permissionRuleset(t, src)
		filtered := yoloRuleset(full)
		for _, toolName := range []string{tools.NameDelegate, tools.NameHandoff, tools.NameCancel} {
			args := []string{"*"}
			if toolName == tools.NameDelegate {
				args = []string{"builder", "reviewer", "tester"}
			}
			for _, arg := range args {
				got := filtered.Evaluate(toolName, arg)
				want := full.Evaluate(toolName, arg)
				if got != want {
					t.Fatalf("ruleset %q: %s(%q) under YOLO = %v, full ruleset = %v", src, toolName, arg, got, want)
				}
			}
		}
	}
}

// TestYoloAskDowngradeScope pins which tools' ask decisions YOLO relaxes:
// ordinary tools and the delegate/handoff/cancel mechanism tools relax, while
// done and compact_context keep their dedicated action semantics.
func TestYoloAskDowngradeScope(t *testing.T) {
	for name, want := range map[string]bool{
		tools.NameDelegate:       true,
		tools.NameHandoff:        true,
		tools.NameCancel:         true,
		tools.NameShell:          true,
		tools.NameRead:           true,
		tools.NameDone:           false,
		tools.NameCompactContext: false,
	} {
		if got := yoloAskDowngradeTool(name); got != want {
			t.Fatalf("yoloAskDowngradeTool(%q) = %v, want %v", name, got, want)
		}
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
				{Permission: tools.NameDone, Pattern: "*", Action: permission.ActionDeny},
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

	for _, toolName := range []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel, tools.NameDone} {
		t.Run(toolName+" protected", func(t *testing.T) {
			call := message.ToolCall{Name: toolName, Args: json.RawMessage(`{}`)}
			err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{})
			if err == nil || !strings.Contains(err.Error(), "denied by permission policy") {
				t.Fatalf("applyPermission(%s) err = %v, want permission denied", toolName, err)
			}
		})
	}
}

func TestYoloBusyToggleDefersPromptAndToolSurfaceUntilNextRequest(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})
	a.ruleset = permission.Ruleset{{Permission: tools.NameGlob, Pattern: "*", Action: permission.ActionDeny}}
	a.sessionBuilt.Store(true)
	a.freezeToolSurface()
	beforePrompt := a.installedSysPrompt
	if _, ok := a.tools.Get(tools.NameGlob); !ok {
		t.Fatal("expected Glob registered")
	}
	if len(a.mainLLMToolDefinitions()) != 0 {
		t.Fatal("initial frozen surface should hide Glob under deny rule")
	}

	a.handleYoloCommand("/yolo on", true)

	if !a.YoloEnabled() {
		t.Fatal("YOLO should enable while busy")
	}
	decision := evaluateToolPermissionInDir(a.effectiveRuleset(), tools.NameGlob, json.RawMessage(`{"patterns":["*"]}`), permission.PathScope{})
	if decision.Action != permission.ActionDeny {
		t.Fatalf("effective Glob action after YOLO = %v, want deny via empty YOLO ruleset", decision.Action)
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed immediately after busy YOLO toggle")
	}
	if frozen := a.frozenToolDefs.Load(); frozen != nil && len(*frozen) != 0 {
		t.Fatalf("frozen tool surface changed immediately: %#v", *frozen)
	}
	if a.sessionBuilt.Load() {
		t.Fatal("sessionBuilt should be reset so next request rebuilds context")
	}

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt: %v", err)
	}
	defs := a.mainLLMToolDefinitions()
	if got := len(defs); got != 1 {
		t.Fatalf("rebuilt tool definitions count = %d, want 1", got)
	}
	if got := defs[0].Name; got != tools.NameGlob {
		t.Fatalf("rebuilt tool = %q, want %q", got, tools.NameGlob)
	}
}

func TestYoloToggleReturningToSameStateKeepsFrozenContext(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})

	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("initial ensureSessionBuilt: %v", err)
	}
	beforePrompt := a.installedSysPrompt
	beforeReminder := a.cachedSessionReminderContent.Load()
	beforeDefs := a.frozenToolDefs.Load()
	if beforeReminder == nil || beforeDefs == nil {
		t.Fatal("initial context surface should be frozen")
	}

	a.handleYoloCommand("/yolo on", true)
	a.handleYoloCommand("/yolo off", true)
	if a.YoloEnabled() {
		t.Fatal("YOLO should be off after toggling back")
	}
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt after unchanged YOLO surface: %v", err)
	}
	if got := a.installedSysPrompt; got != beforePrompt {
		t.Fatalf("system prompt changed after YOLO returned to original state: %q", got)
	}
	if got := a.cachedSessionReminderContent.Load(); got != beforeReminder {
		t.Fatalf("session reminder pointer changed after YOLO returned to original state")
	}
	if got := a.frozenToolDefs.Load(); got != beforeDefs {
		t.Fatalf("frozen tool surface pointer changed after YOLO returned to original state")
	}
	if a.surfaceDirty.Load() {
		t.Fatal("surface dirty flag should clear after unchanged surface comparison")
	}
}

// TestYoloRulesetKeepsNarrowGlobRules pins that YOLO's protected-rule filter
// matches tool names the way the permission engine does — normalization plus
// globs — instead of comparing exact strings. A narrow glob such as
// `compact_*` is a rule about a protected tool and must survive YOLO. The
// wildcard `*` rule does not survive verbatim (it would deny every bypassed
// ordinary tool on the visible surface); it is mirrored onto the
// capability-granting control tools so their default keeps matching the user's
// configuration, and a glob that only reaches unprotected tools (`sh*`) is
// dropped.
func TestYoloRulesetKeepsNarrowGlobRules(t *testing.T) {
	ruleset := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "compact_*", Pattern: "*", Action: permission.ActionDeny},
		{Permission: "handoff*", Pattern: "*", Action: permission.ActionAsk},
		{Permission: "sh*", Pattern: "*", Action: permission.ActionAllow},
	}
	filtered := yoloRuleset(ruleset)
	// The wildcard deny mirrors onto delegate/handoff/cancel, then the two
	// surviving user rules; the `sh*` rule is dropped.
	if len(filtered) != len(yoloCapabilityControlTools)+2 {
		t.Fatalf("YOLO ruleset = %+v, want the wildcard mirrors plus the compact_* and handoff* rules only", filtered)
	}
	if got := compactContextPermissionAction(filtered); got != permission.ActionDeny {
		t.Fatalf("compact_* deny must survive YOLO, got %v", got)
	}
	if got := evaluateToolPermissionInDir(filtered, tools.NameHandoff, json.RawMessage(`{"agent":"planner"}`), permission.PathScope{}); got.Action != permission.ActionAsk {
		t.Fatalf("handoff* ask must survive YOLO, got %v", got.Action)
	}
	// The wildcard deny mirror keeps an allowlist role's default: delegate and
	// cancel stay denied even though the wildcard rule itself is gone.
	if got := evaluateToolPermissionInDir(filtered, tools.NameDelegate, json.RawMessage(`{"agent_type":"builder"}`), permission.PathScope{}); got.Action != permission.ActionDeny {
		t.Fatalf("delegate must keep the wildcard deny default under YOLO, got %v", got.Action)
	}
	if got := evaluateToolPermissionInDir(filtered, tools.NameCancel, json.RawMessage(`{}`), permission.PathScope{}); got.Action != permission.ActionDeny {
		t.Fatalf("cancel must keep the wildcard deny default under YOLO, got %v", got.Action)
	}
	// A glob that only reaches unprotected tools is still dropped.
	for _, rule := range filtered {
		if yoloProtectedPermissionRule(rule) {
			continue
		}
		if rule.Permission == "sh*" || strings.HasPrefix(rule.Permission, "sh") {
			t.Fatalf("Shell rule %+v must be dropped from the YOLO ruleset", rule)
		}
	}
}

// TestYoloProtectedPermissionToolNormalizesNames pins that an alias spelling of
// a protected tool cannot slip past the execution-time bypass.
func TestYoloProtectedPermissionToolNormalizesNames(t *testing.T) {
	for _, name := range []string{tools.NameCompactContext, " " + tools.NameDone, tools.NameHandoff + "\t"} {
		if !yoloProtectedPermissionTool(name) {
			t.Fatalf("%q must stay protected under YOLO", name)
		}
	}
	if yoloProtectedPermissionTool(tools.NameShell) {
		t.Fatal("shell must not be protected under YOLO")
	}
}

// Regression (new adjudication), through the real MainAgent wiring: a
// mechanism ask relaxes to an implicit allow while YOLO is on (no shared
// confirmation dialog) and the dialog returns the moment YOLO switches off.
func TestYoloMainGateMechanismAskRelaxesAndRestores(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permissionRuleset(t, "delegate: ask")
	a.newTurn()
	var confirmCalls atomic.Int32
	a.confirmFn = subYoloConfirmStub(&confirmCalls)

	call := message.ToolCall{Name: tools.NameDelegate, Args: json.RawMessage(`{"agent_type":"worker"}`)}
	if err := a.toolExecutionPipeline().applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
		t.Fatal("mechanism ask must confirm while YOLO is off")
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls with YOLO off = %d, want 1", got)
	}

	a.yoloEnabled.Store(true)
	if err := a.toolExecutionPipeline().applyPermission(context.Background(), &call, &ToolExecutionResult{}); err != nil {
		t.Fatalf("YOLO must relax the mechanism ask to allow, got %v", err)
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls under YOLO = %d, want still 1", got)
	}

	a.yoloEnabled.Store(false)
	if err := a.toolExecutionPipeline().applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
		t.Fatal("mechanism ask must confirm again after YOLO is switched off")
	}
	if got := confirmCalls.Load(); got != 2 {
		t.Fatalf("confirm calls after YOLO off = %d, want 2", got)
	}
}
