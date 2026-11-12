package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// subYoloConfirmStub counts confirm invocations and always rejects, so tests
// can tell whether an ask decision reached the shared confirmation flow.
func subYoloConfirmStub(calls *atomic.Int32) ConfirmFunc {
	return func(context.Context, string, string, []string, []string, []permission.Rule, []permission.Rule) (ConfirmResponse, error) {
		calls.Add(1)
		return ConfirmResponse{Approved: false, DenyReason: "test rejection"}, nil
	}
}

func newSubAgentWithRoleRules(t *testing.T, permissionYAML string) (*MainAgent, *SubAgent) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	a.activeConfig = &config.AgentConfig{Name: "builder"}
	a.agentConfigs = map[string]*config.AgentConfig{
		"worker": {Name: "worker", Permission: parsePermissionNode(t, permissionYAML)},
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-yolo")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs[sub.agentDefName]))
	return a, sub
}

func subYoloApplyPermission(t *testing.T, sub *SubAgent, toolName string) error {
	t.Helper()
	args := json.RawMessage(`{}`)
	switch toolName {
	case tools.NameShell:
		args = json.RawMessage(`{"command":"git status --short"}`)
	case tools.NameWrite:
		args = json.RawMessage(`{"path":"notes.txt"}`)
	case tools.NameDelegate:
		// Delegate decisions match on agent_type; a call without it cannot
		// match any rule and falls back to deny.
		args = json.RawMessage(`{"agent_type":"worker"}`)
	}
	return subYoloApplyPermissionArgs(t, sub, toolName, string(args))
}

func subYoloApplyPermissionArgs(t *testing.T, sub *SubAgent, toolName string, args string) error {
	t.Helper()
	if args == "" {
		args = `{}`
	}
	pipeline := sub.toolExecutionPipeline()
	call := message.ToolCall{Name: toolName, Args: json.RawMessage(args)}
	return pipeline.applyPermission(context.Background(), &call, &ToolExecutionResult{})
}

// TestSubAgentInheritsParentYoloDowngradesAskToAllow locks in that a SubAgent
// inherits the main agent's YOLO mode at the decision point: ask rules for
// ordinary tools stop prompting the user while the parent YOLO is on and go
// back to prompting the moment the parent switches it off.
func TestSubAgentInheritsParentYoloDowngradesAskToAllow(t *testing.T) {
	t.Parallel()
	a, sub := newSubAgentWithRoleRules(t, "shell: ask\n")
	var confirmCalls atomic.Int32
	a.confirmFn = subYoloConfirmStub(&confirmCalls)

	if err := subYoloApplyPermission(t, sub, tools.NameShell); err == nil {
		t.Fatal("ask must confirm while the parent YOLO mode is off")
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls with YOLO off = %d, want 1", got)
	}

	a.yoloEnabled.Store(true)
	if err := subYoloApplyPermission(t, sub, tools.NameShell); err != nil {
		t.Fatalf("inherited YOLO must downgrade ask to allow, got %v", err)
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls under inherited YOLO = %d, want still 1", got)
	}

	a.yoloEnabled.Store(false)
	if err := subYoloApplyPermission(t, sub, tools.NameShell); err == nil {
		t.Fatal("ask must confirm again after the parent switches YOLO off")
	}
	if got := confirmCalls.Load(); got != 2 {
		t.Fatalf("confirm calls after YOLO off = %d, want 2", got)
	}
}

// TestSubAgentYoloInheritanceKeepsDenyGuardrail locks in that inherited YOLO
// relaxes ask only: a configured deny (here a read-only role's Write deny)
// still blocks the SubAgent.
func TestSubAgentYoloInheritanceKeepsDenyGuardrail(t *testing.T) {
	t.Parallel()
	a, sub := newSubAgentWithRoleRules(t, "write: deny\n")
	var confirmCalls atomic.Int32
	a.confirmFn = subYoloConfirmStub(&confirmCalls)
	a.yoloEnabled.Store(true)

	if err := subYoloApplyPermission(t, sub, tools.NameWrite); err == nil {
		t.Fatal("inherited YOLO must not lift a configured Write deny")
	}
	if got := confirmCalls.Load(); got != 0 {
		t.Fatalf("confirm calls = %d, want 0 for a deny decision", got)
	}
}

// TestSubAgentYoloInheritanceRelaxesMechanismAsk locks in that the mechanism
// control tools relax under inherited YOLO exactly as they do on the main
// agent: a delegate ask stops prompting while the parent YOLO is on and goes
// back to prompting the moment the parent switches it off.
func TestSubAgentYoloInheritanceRelaxesMechanismAsk(t *testing.T) {
	t.Parallel()
	a, sub := newSubAgentWithRoleRules(t, "delegate: ask\n")
	var confirmCalls atomic.Int32
	a.confirmFn = subYoloConfirmStub(&confirmCalls)

	if err := subYoloApplyPermission(t, sub, tools.NameDelegate); err == nil {
		t.Fatal("mechanism ask must confirm while the parent YOLO mode is off")
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls with YOLO off = %d, want 1", got)
	}

	a.yoloEnabled.Store(true)
	if err := subYoloApplyPermission(t, sub, tools.NameDelegate); err != nil {
		t.Fatalf("inherited YOLO must relax a mechanism ask, got %v", err)
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls under inherited YOLO = %d, want still 1", got)
	}

	a.yoloEnabled.Store(false)
	if err := subYoloApplyPermission(t, sub, tools.NameDelegate); err == nil {
		t.Fatal("mechanism ask must confirm again after the parent switches YOLO off")
	}
	if got := confirmCalls.Load(); got != 2 {
		t.Fatalf("confirm calls after YOLO off = %d, want 2", got)
	}
}

// TestYoloScopeAsymmetryBetweenMainAgentAndDelegatedWorker pins the two-sided
// YOLO contract where a user meets it: enabling YOLO on the main agent lifts
// call-level friction inside the granted scope, but a worker it delegates to
// evaluates the configured ruleset untouched — its only inherited effect is
// the ask→allow downgrade. The rules under test are the active role's own
// permission config, which reaches the main agent's ruleset through
// rebuildRuleset and a SubAgent's base ruleset through subAgentBaseRuleset.
//
// The asymmetry is per winning-rule shape: an independent scoped deny lifts on
// the main agent only, while a tool-level wildcard deny (a hidden tool) stays
// bound on both sides.
func TestYoloScopeAsymmetryBetweenMainAgentAndDelegatedWorker(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	scopedSrc := "shell: allow\nshell:\n  \"rm *\": deny\n"
	wildcardSrc := "write: deny\n"
	a.agentConfigs = map[string]*config.AgentConfig{
		"planner": {Name: "planner", Permission: parsePermissionNode(t, scopedSrc+wildcardSrc)},
		"worker":  {Name: "worker", Permission: parsePermissionNode(t, scopedSrc)},
	}

	// The active role's own permission config becomes the overlay base, which
	// rebuildRuleset folds into the main agent's ruleset.
	a.activeConfig = a.agentConfigs["planner"]
	a.rebuildRuleset()
	if !a.effectiveRuleset().IsDisabled(tools.NameWrite) {
		t.Fatal("the active role's write deny must reach the main agent ruleset")
	}

	a.newTurn("")
	rmCall := &message.ToolCall{Name: tools.NameShell, Args: json.RawMessage(`{"command":"rm -rf build"}`)}
	writeCall := func() *message.ToolCall {
		return &message.ToolCall{Name: tools.NameWrite, Args: json.RawMessage(`{"path":"notes.txt"}`)}
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-scope")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs["worker"]))

	// YOLO off: both sides honour the configured rules.
	pipeline := a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), rmCall, &ToolExecutionResult{}); err == nil {
		t.Fatal("main agent rm must be denied while YOLO is off")
	}
	if err := pipeline.applyPermission(context.Background(), writeCall(), &ToolExecutionResult{}); err == nil {
		t.Fatal("main agent write must be denied while YOLO is off")
	}
	if err := subYoloApplyPermissionArgs(t, sub, tools.NameShell, `{"command":"rm -rf build"}`); err == nil {
		t.Fatal("worker rm must be denied while YOLO is off")
	}

	a.yoloEnabled.Store(true)

	// The main agent lifts the call-level friction: the independent scoped
	// shell deny relaxes. The wildcard write deny is the capability boundary
	// and keeps rejecting.
	pipeline = a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), rmCall, &ToolExecutionResult{}); err != nil {
		t.Fatalf("main agent rm must be allowed under YOLO, got %v", err)
	}
	if err := pipeline.applyPermission(context.Background(), writeCall(), &ToolExecutionResult{}); err == nil {
		t.Fatal("main agent write must stay denied under YOLO: a wildcard deny hides the tool")
	}

	// The worker's ruleset is the delegation contract, so YOLO must not
	// rewrite it: the scoped deny still blocks there.
	if a.buildSubAgentRuleset(a.agentConfigs["worker"]).Evaluate(tools.NameShell, "rm -rf build") != permission.ActionDeny {
		t.Fatal("YOLO must not lift a scoped deny from a SubAgent's base ruleset")
	}
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs["worker"]))
	if err := subYoloApplyPermissionArgs(t, sub, tools.NameShell, `{"command":"rm -rf build"}`); err == nil {
		t.Fatal("worker rm must stay denied under the parent's YOLO")
	}
}
