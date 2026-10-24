package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestQueuedSubAgentInputRetiresInflightDraft(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.focusedAgentID = "agent-1"
	m.inflightDraft = &queuedDraft{ID: "draft-1", AgentID: "agent-1", Content: "follow up"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SubAgentInputDeliveryEvent{
		AgentID: "agent-1", Status: agent.SubAgentInputDeliveryQueued,
	}})

	if m.inflightDraft != nil {
		t.Fatal("a queued delivery left the draft in flight")
	}
	if m.activeToast == nil || m.activeToast.Message != "Message queued for the next attempt" {
		t.Fatalf("activeToast = %+v, want the queued notice", m.activeToast)
	}
}

func TestSubAgentDeliveryLeavesOtherAgentDraftAlone(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.inflightDraft = &queuedDraft{ID: "draft-1", AgentID: "agent-2", Content: "other"}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SubAgentInputDeliveryEvent{
		AgentID: "agent-1", Status: agent.SubAgentInputDeliveryFailed,
	}})

	if m.inflightDraft == nil {
		t.Fatal("a delivery outcome for another agent retired the draft")
	}
	if m.activeToast != nil {
		t.Fatalf("activeToast = %+v, want none (the agent reports its own failure toast)", m.activeToast)
	}
}
