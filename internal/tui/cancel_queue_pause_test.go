package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
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

func TestIdleAfterCancelRevealsPromptAboveLongInterruptedReply(t *testing.T) {
	backend := &sessionControlAgent{
		messages: []message.Message{
			{Role: message.RoleUser, Content: "keep this prompt visible"},
			{Role: message.RoleAssistant, Content: strings.Repeat("partial reply line\n", 80), StopReason: "interrupted"},
		},
	}
	m := NewModelWithSize(backend, 80, 20)
	m.mode = ModeNormal
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.pauseQueuedDraftDrainOnce = true
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "keep this prompt visible", MsgIndex: 0})
	m.nextBlockID = 2
	_ = m.handleAgentEvent(agentEventMsg{event: agent.StreamTextEvent{Text: strings.Repeat("partial reply line\n", 80)}})
	m.currentAssistantBlock.MsgIndex = 1
	m.recalcViewportSize()
	if m.viewport.offset == 0 {
		t.Fatal("long streaming reply should push the prompt above the viewport before cancel")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.IdleEvent{}})

	userStart, ok := m.viewport.LineOffsetForBlockID(1)
	if !ok {
		t.Fatal("cancelled turn user block not found")
	}
	blocks := m.viewport.visibleBlocks()
	userEnd := userStart + m.viewport.blockSpanAt(blocks, 0, blocks[0])
	if userEnd <= m.viewport.offset || userStart >= m.viewport.offset+m.viewport.height {
		t.Fatalf("cancelled turn user block [%d,%d) is outside viewport [%d,%d)", userStart, userEnd, m.viewport.offset, m.viewport.offset+m.viewport.height)
	}
	if m.viewport.sticky {
		t.Fatal("viewport should stop following the reply tail after revealing the cancelled turn prompt")
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

func TestCancelBusyAgentCancelsBackgroundOwnerActivity(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.focusedAgentID = ""
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityIdle, AgentID: "main"}
	m.activities["owner-1"] = agent.AgentActivityEvent{Type: agent.ActivityWaitingToken, AgentID: "owner-1", Detail: "child event"}

	if cmd := m.cancelBusyAgent(); cmd == nil {
		t.Fatal("cancelBusyAgent returned nil for running background owner")
	}
	if backend.cancelCalls != 1 {
		t.Fatalf("CancelCurrentTurn calls = %d, want 1", backend.cancelCalls)
	}
}
