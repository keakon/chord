package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func TestIdleAfterCancelKeepsQueuedDraftsPaused(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming}
	m.queuedDrafts = []queuedDraft{{
		ID:       "draft-1",
		Content:  "queued draft",
		QueuedAt: time.Now(),
	}}

	// ESC is the cancel key (not Ctrl+C).
	// Simulate ESC in normal mode with busy agent
	if cmd := m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})); cmd == nil {
		t.Fatal("handleNormalKey(ESC) = nil, want cancel command")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})

	if got := len(m.queuedDrafts); got != 1 {
		t.Fatalf("len(queuedDrafts) = %d, want 1", got)
	}
	if got := len(backend.sentMessages); got != 0 {
		t.Fatalf("SendUserMessage() calls = %d, want 0", got)
	}
	if m.inflightDraft != nil {
		t.Fatalf("inflightDraft = %+v, want nil", m.inflightDraft)
	}
	if m.pauseQueuedDraftDrainOnce {
		t.Fatal("pauseQueuedDraftDrainOnce = true after idle, want false")
	}
}

// TestCancelBusyAgentFallsBackWhenTurnAlreadyCleared simulates the post-bug
// state where the agent reports no active turn (CancelCurrentTurn returns
// false) but the UI still shows busy because Cooling/Retrying activity hasn't
// been reset. Without the defensive fallback, esc was a silent no-op. With the
// fallback, the visible activity is forced to Idle and the user gets a warn
// toast.
func TestCancelBusyAgentFallsBackWhenTurnAlreadyCleared(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: false} // simulate "turn already nil" on agent side
	m := NewModel(backend)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityCooling, AgentID: "main", Detail: "30s"}
	m.inflightDraft = &queuedDraft{ID: "stuck", Content: "leftover", QueuedAt: time.Now()}

	cmd := m.cancelBusyAgent()
	if cmd == nil {
		t.Fatal("cancelBusyAgent returned nil; expected fallback toast cmd")
	}
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", backend.cancelCalls)
	}
	if got := m.activities["main"].Type; got != agent.ActivityIdle {
		t.Fatalf("activities[main].Type = %v, want ActivityIdle", got)
	}
	if m.inflightDraft != nil {
		t.Fatalf("inflightDraft = %+v, want nil", m.inflightDraft)
	}
	if !m.pauseQueuedDraftDrainOnce {
		t.Fatal("pauseQueuedDraftDrainOnce should be true to prevent auto-fire on the spurious idle transition")
	}
	if m.activeToast == nil {
		t.Fatal("expected active toast to be set after fallback")
	}
	if !strings.Contains(m.activeToast.Message, "No active turn to cancel") {
		t.Fatalf("activeToast.Message = %q, want it to contain 'No active turn to cancel'", m.activeToast.Message)
	}
	if m.activeToast.Level != "warn" {
		t.Fatalf("activeToast.Level = %q, want %q", m.activeToast.Level, "warn")
	}
}

// TestCancelBusyAgentNoOpWhenIdleActivity makes sure the fallback doesn't fire
// when the UI is genuinely idle. Otherwise pressing esc in a quiet session
// would emit spurious toasts.
func TestCancelBusyAgentNoOpWhenIdleActivity(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: false}
	m := NewModel(backend)
	m.mode = ModeNormal
	// No activity entry → isAgentBusy() should return false → cancelBusyAgent
	// short-circuits before even calling CancelCurrentTurn.
	if cmd := m.cancelBusyAgent(); cmd != nil {
		t.Fatalf("cancelBusyAgent on idle UI returned %v, want nil", cmd)
	}
	if backend.cancelCalls != 0 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 0 on idle UI", backend.cancelCalls)
	}
}
