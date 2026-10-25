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
	pipeline := sub.toolExecutionPipeline()
	call := message.ToolCall{Name: toolName, Args: args}
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
// YOLO contract where a user meets it: enabling YOLO while the main agent runs
// a read-only role makes that role writable, but a worker it delegates to
// stays read-only. The rule under test is the active role's own permission
// config (a planner's `write: deny`), which reaches the main agent's ruleset
// through rebuildRuleset and a SubAgent's base ruleset through
// subAgentBaseRuleset.
//
// This is the only test covering the inheritance path. Its neighbour,
// TestSubAgentYoloInheritanceKeepsDenyGuardrail, puts its deny on the worker's
// own agent definition, so it cannot see a change that lets YOLO's rule filter
// reach the SubAgent base — such as making subAgentBaseRuleset return
// effectiveRuleset instead of snapshotRuleset, which would silently make every
// delegated worker writable under YOLO.
func TestYoloScopeAsymmetryBetweenMainAgentAndDelegatedWorker(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.agentConfigs = map[string]*config.AgentConfig{
		"planner": {Name: "planner", Permission: parsePermissionNode(t, "write: deny\n")},
		"worker":  {Name: "worker"},
	}

	// A read-only active role: its own permission config becomes the overlay
	// base, which rebuildRuleset folds into the main agent's ruleset.
	a.activeConfig = a.agentConfigs["planner"]
	a.rebuildRuleset()
	if !a.snapshotRuleset().IsDisabled(tools.NameWrite) {
		t.Fatal("the active role's write deny must reach the main agent ruleset")
	}

	a.newTurn("")
	writeCall := func() *message.ToolCall {
		return &message.ToolCall{Name: tools.NameWrite, Args: json.RawMessage(`{"path":"notes.txt"}`)}
	}
	sub := newControllableTestSubAgent(t, a, "adhoc-scope")
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs["worker"]))

	// YOLO off: both sides honour the active role's deny.
	pipeline := a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), writeCall(), &ToolExecutionResult{}); err == nil {
		t.Fatal("main agent write must be denied while YOLO is off")
	}
	if err := subYoloApplyPermission(t, sub, tools.NameWrite); err == nil {
		t.Fatal("worker write must be denied while YOLO is off")
	}

	a.yoloEnabled.Store(true)

	// The main agent's ordinary permission limits are lifted, including the
	// ones coming from the active role's own config.
	if a.effectiveRuleset().IsDisabled(tools.NameWrite) {
		t.Fatal("YOLO must lift the active role's own write deny on the main agent")
	}
	pipeline = a.toolExecutionPipeline()
	if err := pipeline.applyPermission(context.Background(), writeCall(), &ToolExecutionResult{}); err != nil {
		t.Fatalf("main agent write must be allowed under YOLO, got %v", err)
	}

	// The worker's ruleset is the delegation contract, so YOLO must not filter
	// it: deny still blocks, and only the ask→allow downgrade crosses over.
	if !a.buildSubAgentRuleset(a.agentConfigs["worker"]).IsDisabled(tools.NameWrite) {
		t.Fatal("YOLO must not drop the active role's deny from a SubAgent's base ruleset")
	}
	sub.setRuleset(a.buildSubAgentRuleset(a.agentConfigs["worker"]))
	if err := subYoloApplyPermission(t, sub, tools.NameWrite); err == nil {
		t.Fatal("worker write must stay denied under the parent's YOLO")
	}
}
