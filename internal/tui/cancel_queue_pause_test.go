package tui

import (
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

	// Phase A: ESC is the cancel key (not Ctrl+C)
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
