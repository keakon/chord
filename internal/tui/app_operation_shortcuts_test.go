package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func TestStopShortcutCancelsWithoutLosingDraftOrQuitting(t *testing.T) {
	for _, mode := range []Mode{ModeInsert, ModeNormal} {
		backend := &sessionControlAgent{cancelResult: true, loopState: agent.LoopStateExecuting}
		m := NewModelWithSize(backend, 80, 24)
		m.mode = mode
		m.input.SetValue("Unsent draft")
		m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
		m.handleModeKey(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
		if backend.cancelCalls != 1 || backend.loopDisableCalls != 1 {
			t.Fatalf("stop calls = %d, loop disable = %d", backend.cancelCalls, backend.loopDisableCalls)
		}
		if m.input.Value() != "Unsent draft" || m.quitting || m.mode != mode {
			t.Fatal("stop changed input, mode or session")
		}
	}
}

func TestRoleShortcutOnWorkerDoesNotChangeMainRole(t *testing.T) {
	backend := &sessionControlAgent{currentRole: "builder", availableRoles: []string{"builder", "planner"}}
	m := NewModelWithSize(backend, 80, 24)
	m.focusedAgentID = "worker-1"
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModAlt}))
	if backend.currentRole != "builder" || m.focusedAgentID != "worker-1" || m.activeToast == nil {
		t.Fatal("role shortcut must explain main-view requirement")
	}
}

func TestStopFromWorkerViewAlsoDisablesLoop(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true, loopState: agent.LoopStateExecuting}
	m := NewModelWithSize(backend, 80, 24)
	m.focusedAgentID = "worker-1"
	m.activities["worker-1"] = agent.AgentActivityEvent{AgentID: "worker-1", Type: agent.ActivityStreaming}
	m.stopCurrentOperation()
	if backend.loopDisableCalls != 1 || backend.cancelCalls != 1 {
		t.Fatal("stop from worker must stop the turn and its loop")
	}
}

func TestStopBindingCanBeRemapped(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModelWithSize(backend, 80, 24)
	m.keyMap = KeyMapFromConfig(map[string][]string{"stop": {"alt+x"}})
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModAlt}))
	if backend.cancelCalls != 1 {
		t.Fatal("configured stop binding did not cancel")
	}
}
