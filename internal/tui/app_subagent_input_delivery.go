package tui

import (
	"slices"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func (m *Model) settleSubAgentInputDelivery(evt agent.SubAgentInputDeliveryEvent) tea.Cmd {
	if !m.inflightDraftBelongsToAgent(evt.AgentID) || evt.RequestID != "" && m.inflightDraft.ID != evt.RequestID {
		return nil
	}
	draft := *m.inflightDraft
	m.inflightDraft = nil
	if evt.Status != agent.SubAgentInputDeliveryFailed {
		return nil
	}
	if evt.RequestID != "" {
		match := func(block *Block) bool {
			return block.Type == BlockUser && block.InputRequestID == evt.RequestID && normalizeDraftAgentID(block.AgentID) == normalizeDraftAgentID(evt.AgentID)
		}
		blockID := -1
		if m.viewport != nil {
			for _, block := range slices.Backward(m.viewport.blocks) {
				if match(block) {
					blockID = block.ID
					break
				}
			}
		}
		if state := m.startupDeferredTranscript; blockID < 0 && state != nil {
			for _, block := range slices.Backward(state.allBlocks) {
				if block != nil && match(block) {
					blockID = block.ID
					break
				}
			}
		}
		m.removeViewportBlockByID(blockID)
	}
	if !m.hasComposerContent() && normalizeDraftAgentID(m.focusedAgentID) == normalizeDraftAgentID(draft.AgentID) {
		return m.loadQueuedDraftIntoComposer(draft)
	}
	// Do not overwrite newer composer input or automatically resend a refusal.
	draft.Withdrawn = true
	draft.DeliveryStatus = "Not sent"
	m.queuedDrafts = append(m.queuedDrafts, draft)
	m.recalcViewportSize()
	return nil
}
