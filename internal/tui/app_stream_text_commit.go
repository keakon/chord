package tui

import (
	"time"

	"github.com/keakon/chord/internal/agent"
)

// commitStreamText reconciles a request's rendered text with its final response.
// It can create a card for a terminal-only response, but never reopens a settled
// segment whose card is absent or displaces a newer segment's live card.
func (m *Model) commitStreamText(evt agent.StreamTextCommitEvent) agentEventEffects {
	var effects agentEventEffects
	incoming := streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq}
	state := m.streamState(evt.AgentID)
	var card *Block
	if state.assistant != nil && blockStreamSegmentIdentity(state.assistant) == incoming {
		card = state.assistant
	} else if evt.Text != "" || m.streamSegmentSettled(evt.AgentID, incoming) {
		// Empty tool-only replies have no live card. Only a settled segment can
		// need a late empty retraction of a card detached from the live state.
		card = m.streamCardForSegment(evt.AgentID, BlockAssistant, incoming)
	}
	if card == nil {
		if evt.Text == "" || !incoming.known() || m.streamSegmentSettled(evt.AgentID, incoming) {
			return effects
		}
		for _, live := range []*Block{state.assistant, state.thinking} {
			if live != nil && blockStreamSegmentIdentity(live).after(incoming) {
				return effects
			}
		}
		if state.assistant != nil {
			m.finalizeAgentStream(evt.AgentID)
			state = m.streamState(evt.AgentID)
		}
		card = &Block{ID: m.nextBlockID, Type: BlockAssistant, Streaming: true,
			AgentID: evt.AgentID, StartedAt: time.Now(), MsgIndex: m.streamCommitIndex(evt.AgentID),
			StreamTurnID: incoming.turnID, StreamRequestSeq: incoming.requestSeq}
		m.nextBlockID++
		state.assistant = card
		state.assistantAppended = false
	}
	if evt.Text == "" {
		m.removeViewportBlockByID(card.ID)
		if state.assistant == card {
			state.assistant = nil
			state.assistantAppended = false
		}
		// Empty terminal text is a real confirmation, not a missing update.
		m.markStreamSegmentSettled(evt.AgentID, incoming)
	} else {
		if !card.spillCold && card.Content == evt.Text && card.streamAccumulatedContent() == evt.Text &&
			(state.assistant != card || state.assistantAppended) {
			card.streamContentBuilder = nil
			return effects
		}
		// The entire assistant payload is available here. Invalidate its old spill
		// snapshot without synchronous disk reads; the next eviction writes the
		// corrected content. Transcript persistence does not update spill files.
		card.spillCold = false
		card.spillRef = nil
		card.spillStore = nil
		card.spillSummary = ""
		card.spillIdentityKey = ""
		card.spillLineCounts = nil
		card.Content = evt.Text
		card.streamContentBuilder = nil
		card.InvalidateCache()
		if state.assistant == card && !state.assistantAppended {
			m.appendViewportBlock(card)
			state.assistantAppended = true
			effects.addFollowup(m.requestStreamBoundaryFlush())
		} else {
			m.updateViewportBlock(card)
		}
	}
	m.storeStreamState(evt.AgentID, state)
	m.exitRenderFreeze()
	m.markStreamRenderDirty()
	effects.addFollowup(m.scheduleStreamFlush(0))
	return effects
}
