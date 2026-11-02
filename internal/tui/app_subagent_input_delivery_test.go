package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestRejectedSubAgentInputRestoresDraft(t *testing.T) {
	for _, newer := range []bool{false, true} {
		m := NewModelWithSize(nil, 100, 24)
		m.focusedAgentID = "sample-agent"
		draft := queuedDraft{ID: "request-1", AgentID: "sample-agent", Content: "Follow-up.", Parts: []message.ContentPart{
			{Type: message.ContentPartText, Text: "Follow-up."},
			{Type: message.ContentPartImage, MimeType: "image/png", Data: []byte("image")},
		}}
		m.stageDraft(draft)
		if newer {
			m.input.SetValue("Newer draft.")
		}
		m.handleAgentEvent(agentEventMsg{event: agent.SubAgentInputDeliveryEvent{AgentID: "sample-agent", RequestID: "request-1", Status: agent.SubAgentInputDeliveryFailed}})
		if m.inflightDraft != nil || len(m.viewport.blocks) != 0 {
			t.Fatal("rejected optimistic input was not retired")
		}
		if newer {
			if m.input.Value() != "Newer draft." || len(m.queuedDrafts) != 1 || !m.queuedDrafts[0].Withdrawn {
				t.Fatal("refusal overwrote newer input or lost the rejected draft")
			}
		} else if m.input.Value() == "" || len(m.attachments) != 1 {
			t.Fatal("rejected multipart draft was not restored")
		}
	}
}

func TestSubAgentDeliveryReceiptDoesNotRetireNewerSubmission(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.inflightDraft = &queuedDraft{ID: "request-2", AgentID: "sample-agent"}
	m.handleAgentEvent(agentEventMsg{event: agent.SubAgentInputDeliveryEvent{AgentID: "sample-agent", RequestID: "request-1", Status: agent.SubAgentInputDeliveryFailed}})
	if m.inflightDraft == nil || m.inflightDraft.ID != "request-2" {
		t.Fatal("old rejection retired the newer submission")
	}
}

func TestRejectedSubAgentInputDoesNotRemoveHistoricalMatchingCard(t *testing.T) {
	m := NewModelWithSize(nil, 100, 24)
	m.focusedAgentID = "sample-agent"
	draft := queuedDraft{ID: "request-1", AgentID: "sample-agent", Content: "Follow-up."}
	m.stageDraft(draft)
	historical := m.viewport.blocks[0]
	m.inflightDraft = nil
	m.stageDraft(draft)
	current := m.viewport.blocks[1]
	m.startupDeferredTranscript = &startupDeferredTranscriptState{allBlocks: []*Block{historical, current}}
	m.handleAgentEvent(agentEventMsg{event: agent.SubAgentInputDeliveryEvent{AgentID: "sample-agent", RequestID: draft.ID, Status: agent.SubAgentInputDeliveryFailed}})
	if len(m.viewport.blocks) != 1 || m.viewport.blocks[0].ID != historical.ID {
		t.Fatal("refusal removed the historical matching card instead of the in-flight card")
	}
	if m.startupDeferredBlockByID(current.ID) != nil {
		t.Fatal("refusal left the optimistic card in the deferred transcript")
	}
}
