package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// subYoloConfirmStub counts confirm invocations and always rejects, so tests
// can tell whether an ask decision reached the shared confirmation flow.
func subYoloConfirmStub(calls *atomic.Int32) ConfirmFunc {
	return func(context.Context, string, string, []string, []string, []string, []string) (ConfirmResponse, error) {
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

// TestSubAgentYoloInheritanceKeepsProtectedToolsAsking locks in that the
// capability-granting control tools stay protected from YOLO: a delegate ask
// still reaches the shared confirmation flow under the parent YOLO.
func TestSubAgentYoloInheritanceKeepsProtectedToolsAsking(t *testing.T) {
	a, sub := newSubAgentWithRoleRules(t, "delegate: ask\n")
	var confirmCalls atomic.Int32
	a.confirmFn = subYoloConfirmStub(&confirmCalls)
	a.yoloEnabled.Store(true)

	if err := subYoloApplyPermission(t, sub, tools.NameDelegate); err == nil {
		t.Fatal("inherited YOLO must not downgrade a protected delegate ask")
	}
	if got := confirmCalls.Load(); got != 1 {
		t.Fatalf("confirm calls = %d, want 1 for a protected tool", got)
	}
}
