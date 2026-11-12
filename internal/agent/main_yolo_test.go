package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// TestYoloExecutionRulesetTransformationTable walks the YOLO authorization
// transformation row by row. Each case evaluates the same (tool, args) against
// the raw ruleset and against the YOLO-transformed ruleset, so the delta is
// exactly the table: capability boundaries (no-match, wildcard deny,
// allowlist-structure scoped deny, delegate/skill scoped deny) keep rejecting,
// while confirmation and call-level friction (ask, independent scoped deny)
// relaxes to allow.
func TestYoloExecutionRulesetTransformationTable(t *testing.T) {
	cases := []struct {
		name     string
		yaml     string
		tool     string
		args     string
		wantRaw  permission.Action
		wantYolo permission.Action
	}{
		{
			name:     "no match in a non-empty ruleset stays denied",
			yaml:     "read: allow",
			tool:     tools.NameShell,
			args:     `{"command":"ls"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionDeny,
		},
		{
			name:     "tool-level wildcard deny stays denied",
			yaml:     "shell: deny\nread: allow",
			tool:     tools.NameShell,
			args:     `{"command":"ls"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionDeny,
		},
		{
			name:     "global wildcard deny (allowlist default) stays denied",
			yaml:     "\"*\": deny\nread: allow",
			tool:     tools.NameShell,
			args:     `{"command":"ls"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionDeny,
		},
		{
			name:     "scoped deny inside an allowlist structure stays denied",
			yaml:     "\"*\": deny\nshell:\n  \"git *\": allow\n  \"rm *\": deny",
			tool:     tools.NameShell,
			args:     `{"command":"rm -rf build"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionDeny,
		},
		{
			name:     "allowlist grant stays allowed",
			yaml:     "\"*\": deny\nshell:\n  \"git *\": allow\n  \"rm *\": deny",
			tool:     tools.NameShell,
			args:     `{"command":"git status --short"}`,
			wantRaw:  permission.ActionAllow,
			wantYolo: permission.ActionAllow,
		},
		{
			name:     "delegate scoped deny (directory membership) stays denied",
			yaml:     "\"*\": allow\ndelegate:\n  reviewer: deny",
			tool:     tools.NameDelegate,
			args:     `{"agent_type":"reviewer"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionDeny,
		},
		{
			name:     "skill scoped deny (directory membership) stays denied",
			yaml:     "\"*\": allow\nskill:\n  go-expert: deny",
			tool:     tools.NameSkill,
			args:     `{"name":"go-expert"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionDeny,
		},
		{
			name:     "independent scoped deny relaxes to allow",
			yaml:     "shell: allow\nshell:\n  \"rm *\": deny",
			tool:     tools.NameShell,
			args:     `{"command":"rm -rf build"}`,
			wantRaw:  permission.ActionDeny,
			wantYolo: permission.ActionAllow,
		},
		{
			name:     "scoped ask relaxes to allow",
			yaml:     "shell:\n  \"git *\": ask",
			tool:     tools.NameShell,
			args:     `{"command":"git status --short"}`,
			wantRaw:  permission.ActionAsk,
			wantYolo: permission.ActionAllow,
		},
		{
			name:     "tool-level ask relaxes to allow",
			yaml:     "shell: ask",
			tool:     tools.NameShell,
			args:     `{"command":"ls"}`,
			wantRaw:  permission.ActionAsk,
			wantYolo: permission.ActionAllow,
		},
		{
			name:     "compact_context explicit ask relaxes to allow",
			yaml:     "compact_context: ask",
			tool:     tools.NameCompactContext,
			args:     `{}`,
			wantRaw:  permission.ActionAsk,
			wantYolo: permission.ActionAllow,
		},
		{
			name:     "allow stays allowed",
			yaml:     "shell: allow",
			tool:     tools.NameShell,
			args:     `{"command":"ls"}`,
			wantRaw:  permission.ActionAllow,
			wantYolo: permission.ActionAllow,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := permissionRuleset(t, tc.yaml)
			if got := evaluateToolPermissionInDir(raw, tc.tool, json.RawMessage(tc.args), permission.PathScope{}).Action; got != tc.wantRaw {
				t.Fatalf("raw action = %v, want %v", got, tc.wantRaw)
			}
			relaxed := raw.RelaxCalls()
			if got := evaluateToolPermissionInDir(relaxed, tc.tool, json.RawMessage(tc.args), permission.PathScope{}).Action; got != tc.wantYolo {
				t.Fatalf("YOLO action = %v, want %v", got, tc.wantYolo)
			}
		})
	}
}

func TestYoloExecutionRulesetEmptyInputReturnsNil(t *testing.T) {
	if got := (permission.Ruleset(nil)).RelaxCalls(); got != nil {
		t.Fatalf("RelaxCalls(nil) = %v, want nil", got)
	}
	if got := (permission.Ruleset{}).RelaxCalls(); got != nil {
		t.Fatalf("RelaxCalls(empty) = %v, want nil", got)
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

// TestYoloAskDowngradeScope pins which tools' ask decisions the SubAgent
// inheritance relaxes: ordinary tools and the delegate/handoff/cancel mechanism
// tools relax, while done and compact_context keep their dedicated action
// semantics.
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
		if got := toolAskPolicyApplies(name); got != want {
			t.Fatalf("toolAskPolicyApplies(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestYoloTransformationCoversCompositeDependencies pins that apply_patch's
// per-operation write/delete dependency checks and generate_image's read/write
// resource checks decide with the same YOLO transformation as the main call:
// a scoped deny lifts only when the dependency has its own tool-wide grant.
func TestYoloTransformationCoversCompositeDependencies(t *testing.T) {
	// Add sections need a "+ "-prefixed content line; Delete takes none.
	// A malformed patch would fail ApplyPatchDisplayTargets and fall back to
	// the whole-tool evaluation, skipping the dependency check under test.
	patchArgs := func(op string) json.RawMessage {
		body := ""
		if op == "Add" {
			body = `+notes\n`
		}
		return json.RawMessage(`{"patch":"*** Begin Patch\n*** ` + op + ` File: drafts/notes.md\n` + body + `"}`)
	}
	scoped := permissionRuleset(t, "apply_patch: allow\nwrite:\n  \"drafts/*\": deny")

	// Without YOLO the write-specific deny makes the whole patch reject.
	if got := evaluateToolPermissionInDir(scoped, tools.NameApplyPatch, patchArgs("Add"), permission.PathScope{}).Action; got != permission.ActionDeny {
		t.Fatalf("baseline apply_patch add with a scoped write deny = %v, want deny", got)
	}
	if got := evaluateToolPermissionInDir(scoped.RelaxCalls(), tools.NameApplyPatch, patchArgs("Add"), permission.PathScope{}).Action; got != permission.ActionDeny {
		t.Fatalf("apply_patch add under YOLO without a write grant = %v, want deny", got)
	}
	grantedWrite := permissionRuleset(t, "apply_patch: allow\nwrite:\n  \"*\": allow\n  \"drafts/*\": deny")
	if got := evaluateToolPermissionInDir(grantedWrite.RelaxCalls(), tools.NameApplyPatch, patchArgs("Add"), permission.PathScope{}).Action; got != permission.ActionAllow {
		t.Fatalf("apply_patch add under YOLO with a write grant = %v, want allow", got)
	}
	// A delete operation depends on the delete rules, not write: its wildcard
	// deny (the hidden standalone tool) keeps rejecting.
	wildcardDelete := permissionRuleset(t, "apply_patch: allow\ndelete: deny")
	if got := evaluateToolPermissionInDir(wildcardDelete.RelaxCalls(), tools.NameApplyPatch, patchArgs("Delete"), permission.PathScope{}).Action; got != permission.ActionDeny {
		t.Fatalf("apply_patch delete under YOLO with delete wildcard-denied = %v, want deny", got)
	}

	// The output_path write resource needs its own grant; without one it is a
	// no-match deny for every ruleset below.
	imageArgs := func() json.RawMessage {
		return json.RawMessage(`{"prompt":"a lighthouse at dawn","reference_images":["refs/shore.png"],"output_path":"out/shore.png"}`)
	}
	scopedRead := permissionRuleset(t, "generate_image: allow\nwrite: allow\nread:\n  \"refs/*\": deny")
	wildcardRead := permissionRuleset(t, "generate_image: allow\nwrite: allow\nread: deny")

	if got := evaluateToolPermissionInDir(scopedRead.RelaxCalls(), tools.NameGenerateImage, imageArgs(), permission.PathScope{}).Action; got != permission.ActionDeny {
		t.Fatalf("generate_image under YOLO without a read grant = %v, want deny", got)
	}
	grantedRead := permissionRuleset(t, "generate_image: allow\nwrite: allow\nread:\n  \"*\": allow\n  \"refs/*\": deny")
	if got := evaluateToolPermissionInDir(grantedRead.RelaxCalls(), tools.NameGenerateImage, imageArgs(), permission.PathScope{}).Action; got != permission.ActionAllow {
		t.Fatalf("generate_image under YOLO with a read grant = %v, want allow", got)
	}
	if got := evaluateToolPermissionInDir(wildcardRead.RelaxCalls(), tools.NameGenerateImage, imageArgs(), permission.PathScope{}).Action; got != permission.ActionDeny {
		t.Fatalf("generate_image under YOLO with read wildcard-denied = %v, want deny", got)
	}
}

// TestYoloToggleLeavesPromptAndToolSurfaceByteIdentical pins the frozen-surface
// contract: toggling YOLO never changes the system prompt or the tool
// declarations, immediately or after the next request rebuild.
func TestYoloToggleLeavesPromptAndToolSurfaceByteIdentical(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.markAgentsMDReady()
	a.MarkSkillsReady()
	a.markMCPReady()
	a.tools.Register(tools.GlobTool{})
	a.ruleset = permissionRuleset(t, "glob: ask")
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("initial ensureSessionBuilt: %v", err)
	}

	promptBefore := a.installedSysPrompt
	defsBefore := a.mainLLMToolDefinitions()
	defsJSONBefore := mustJSONText(t, defsBefore)
	if len(defsBefore) != 1 {
		t.Fatalf("initial tool definitions = %d, want 1", len(defsBefore))
	}

	a.handleYoloCommand("/yolo on", true)
	if !a.YoloEnabled() {
		t.Fatal("YOLO should be on")
	}
	if got := a.installedSysPrompt; got != promptBefore {
		t.Fatalf("system prompt changed after toggling YOLO on")
	}
	if got := mustJSONText(t, a.mainLLMToolDefinitions()); defsJSONBefore != got {
		t.Fatalf("tool definitions changed after toggling YOLO on")
	}
	if a.surfaceDirty.Load() {
		t.Fatal("YOLO toggle must not mark the runtime surface dirty")
	}
	if err := a.ensureSessionBuilt(context.Background()); err != nil {
		t.Fatalf("ensureSessionBuilt after YOLO on: %v", err)
	}
	if got := a.installedSysPrompt; got != promptBefore {
		t.Fatalf("system prompt changed after the post-toggle rebuild")
	}
	if got := mustJSONText(t, a.mainLLMToolDefinitions()); defsJSONBefore != got {
		t.Fatalf("tool definitions changed after the post-toggle rebuild")
	}

	a.handleYoloCommand("/yolo off", true)
	if got := a.installedSysPrompt; got != promptBefore {
		t.Fatalf("system prompt changed after toggling YOLO off")
	}
	if got := mustJSONText(t, a.mainLLMToolDefinitions()); defsJSONBefore != got {
		t.Fatalf("tool definitions changed after toggling YOLO off")
	}
}

// TestYoloMainGateAskRelaxesAndRestores runs the real MainAgent wiring: a rule
// ask (mechanism or ordinary) relaxes to an implicit allow while YOLO is on —
// no shared confirmation dialog — and the dialog returns the moment YOLO
// switches off.
func TestYoloMainGateAskRelaxesAndRestores(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permissionRuleset(t, "delegate: ask\nshell: ask")
	a.newTurn("")
	var confirmCalls int
	a.confirmFn = func(context.Context, string, string, []string, []string, []permission.Rule, []permission.Rule) (ConfirmResponse, error) {
		confirmCalls++
		return ConfirmResponse{Approved: false, DenyReason: "test rejection"}, nil
	}

	calls := []message.ToolCall{
		{Name: tools.NameDelegate, Args: json.RawMessage(`{"agent_type":"worker"}`)},
		{Name: tools.NameShell, Args: json.RawMessage(`{"command":"git status --short"}`)},
	}
	for _, call := range calls {
		pipeline := a.toolExecutionPipeline()
		if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
			t.Fatalf("%s ask must confirm while YOLO is off", call.Name)
		}
	}
	if confirmCalls != len(calls) {
		t.Fatalf("confirm calls with YOLO off = %d, want %d", confirmCalls, len(calls))
	}

	a.yoloEnabled.Store(true)
	for _, call := range calls {
		pipeline := a.toolExecutionPipeline()
		if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err != nil {
			t.Fatalf("YOLO must relax the %s ask to allow, got %v", call.Name, err)
		}
	}
	if confirmCalls != len(calls) {
		t.Fatalf("confirm calls under YOLO = %d, want still %d", confirmCalls, len(calls))
	}

	a.yoloEnabled.Store(false)
	for _, call := range calls {
		pipeline := a.toolExecutionPipeline()
		if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
			t.Fatalf("%s ask must confirm again after YOLO is switched off", call.Name)
		}
	}
	if confirmCalls != 2*len(calls) {
		t.Fatalf("confirm calls after YOLO off = %d, want %d", confirmCalls, 2*len(calls))
	}
}

// TestYoloMainGateIndependentScopedDenyLiftsAndRestores runs the new lifting
// through the real gate: an independent scoped deny is call-level friction and
// relaxes while YOLO is on, then rejects again once it is off.
func TestYoloMainGateIndependentScopedDenyLiftsAndRestores(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permissionRuleset(t, "shell: allow\nshell:\n  \"rm *\": deny")
	a.newTurn("")

	call := message.ToolCall{Name: tools.NameShell, Args: json.RawMessage(`{"command":"rm -rf build"}`)}
	pipeline := a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
		t.Fatal("scoped deny must reject while YOLO is off")
	}

	a.yoloEnabled.Store(true)
	pipeline = a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err != nil {
		t.Fatalf("YOLO must lift an independent scoped deny, got %v", err)
	}

	a.yoloEnabled.Store(false)
	pipeline = a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
		t.Fatal("scoped deny must reject again after YOLO is switched off")
	}
}

// TestYoloMainGateWildcardDenyStaysDenied pins the capability boundary: a
// tool-level wildcard deny means the tool is not granted at all, so YOLO keeps
// rejecting it instead of redefining the role.
func TestYoloMainGateWildcardDenyStaysDenied(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permissionRuleset(t, "\"*\": deny\nread: allow")
	a.yoloEnabled.Store(true)
	a.newTurn("")

	for _, call := range []message.ToolCall{
		{Name: tools.NameShell, Args: json.RawMessage(`{"command":"ls"}`)},
		{Name: tools.NameWrite, Args: json.RawMessage(`{"path":"notes.txt"}`)},
		{Name: tools.NameDelegate, Args: json.RawMessage(`{"agent_type":"worker"}`)},
	} {
		pipeline := a.toolExecutionPipeline()
		if err := pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{}); err == nil {
			t.Fatalf("wildcard deny must keep rejecting %s under YOLO", call.Name)
		}
	}
}

// TestYoloToggleInvalidatesSpeculativeApprovalCache pins that a recorded
// non-interactive allow never survives a YOLO toggle: the gate's release set
// changes with the mode, so the speculative-reuse prefilter must re-evaluate.
func TestYoloToggleInvalidatesSpeculativeApprovalCache(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.ruleset = permissionRuleset(t, "read: allow")
	a.newTurn("")
	a.recordPermissionApproval(a.currentTurn(), "call-1", tools.NameRead, `{"path":"notes.txt"}`, a.effectiveToolBaseDir())

	if !a.permissionApprovalMatches("call-1", tools.NameRead, `{"path":"notes.txt"}`, a.effectiveToolBaseDir(), toolPermissionContext{}) {
		t.Fatal("recorded allow should match before the toggle")
	}
	a.yoloEnabled.Store(true)
	if a.permissionApprovalMatches("call-1", tools.NameRead, `{"path":"notes.txt"}`, a.effectiveToolBaseDir(), toolPermissionContext{}) {
		t.Fatal("recorded allow must not match after toggling YOLO on")
	}
	a.yoloEnabled.Store(false)
	if !a.permissionApprovalMatches("call-1", tools.NameRead, `{"path":"notes.txt"}`, a.effectiveToolBaseDir(), toolPermissionContext{}) {
		t.Fatal("recorded allow should match again once YOLO is back off: the gate set is identical to the recorded one")
	}
}

// TestYoloToggleReturningToSameStateKeepsFrozenContext verifies that an on/off
// round trip leaves the frozen context surface untouched.
func TestYoloToggleReturningToSameStateKeepsFrozenContext(t *testing.T) {
	t.Parallel()
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
