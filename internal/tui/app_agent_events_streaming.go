package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/recovery"
)

func (b *Block) appendStreamingContent(delta string) {
	if b == nil || delta == "" {
		return
	}
	if b.streamContentBuilder == nil {
		b.streamContentBuilder = &strings.Builder{}
		b.streamContentBuilder.Grow(len(b.Content) + len(delta))
		b.streamContentBuilder.WriteString(b.Content)
	}
	b.streamContentBuilder.WriteString(delta)
}

// streamAccumulatedContent returns the streamed text including deltas that
// have not been flushed into Content yet.
func (b *Block) streamAccumulatedContent() string {
	if b == nil {
		return ""
	}
	if b.streamContentBuilder == nil {
		return b.Content
	}
	return b.streamContentBuilder.String()
}

func (b *Block) syncStreamingContent() bool {
	if b == nil || b.streamContentBuilder == nil {
		return false
	}
	content := b.streamContentBuilder.String()
	if b.Content == content {
		return false
	}
	b.Content = content
	return true
}

func (b *Block) finishStreamingContent() {
	b.syncStreamingContent()
	if b != nil {
		b.streamContentBuilder = nil
	}
}

// replaceFinalSubAgentThinkingPayload handles the complete thinking block a
// SubAgent reducer emits at thinking_end. A tool card may have kept the
// existing streaming block alive, so the final payload must replace the
// accumulated delta rather than being appended to it. It reports whether the
// replacement applied, which requires a SubAgent event (AgentID set) and a
// live builder. A blank payload never replaces accumulated content: SubAgent
// reducers commit full text and skip empty blocks, so a blank thinking_end
// here means an emitter changed and must not erase what already streamed.
func (b *Block) replaceFinalSubAgentThinkingPayload(agentID, text string) bool {
	if agentID == "" || b == nil || b.streamContentBuilder == nil || strings.TrimSpace(text) == "" {
		return false
	}
	b.Content = text
	b.streamContentBuilder.Reset()
	b.streamContentBuilder.WriteString(text)
	return true
}

func (m *Model) flushStreamingBlock(block *Block, updateViewport bool) bool {
	if block == nil || !block.syncStreamingContent() {
		return false
	}
	block.InvalidateCache()
	if updateViewport && m != nil && m.viewport != nil {
		m.viewport.UpdateBlock(block.ID)
	}
	return true
}

// currentMainAssistantMsgIndex returns the main agent's transcript length, the
// index at which the main agent's next streamed message will commit. The main
// transcript must be read through the targeted controller: GetMessages() returns
// only the focused agent's transcript, so while the user views a SubAgent it
// would report the SubAgent's length. Main thinking/rollback cards would then
// carry a SubAgent-derived index, breaking rollback matching and translation
// lookups that key on the real main message index. Only low-frequency boundary
// points (request cycle start, card creation, rollback) may call this.
func (m *Model) currentMainAssistantMsgIndex() int {
	if m == nil || m.agent == nil {
		return -1
	}
	if targeted, ok := m.agent.(agent.TargetedConversationController); ok {
		msgs := targeted.GetMessagesForTarget(agent.ConversationTarget{AgentID: identity.MainAgentID})
		return len(msgs)
	}
	// Fallback for a backend without the targeted controller: its GetMessages
	// can only be trusted while the main agent is the focused transcript. Under
	// any other focus the main boundary is unknown, so report -1 — an unknown
	// index keeps rollback conservative (it clears every settled main thinking
	// card) instead of mis-attributing the focused transcript's length.
	if m.focusedAgentID == "" {
		return len(m.agent.GetMessages())
	}
	return -1
}

// streamCommitIndex returns the transcript index at which the in-flight
// assistant/thinking message of agentID will commit, or -1 when it cannot be
// known. The agent exposes only the focused agent's transcript, and sub-agent
// stream events reach the TUI only while that agent is focused, so the current
// message count is exactly the commit position of the next message. A card
// whose stream began while its agent was not the focused transcript gets -1:
// without a reliable boundary, stale-card matching must never infer identity
// from text the card shares with an earlier committed row.
func (m *Model) streamCommitIndex(agentID string) int {
	if agentID == "" || m == nil || m.agent == nil || m.focusedAgentID != agentID {
		return -1
	}
	return len(m.agent.GetMessages())
}

func (m *Model) ensureStreamingThinkingBlock(agentID string, state *agentStreamState) *Block {
	if state.thinking != nil {
		// A card created by an out-of-focus start event has no commit boundary
		// yet; the first delta while its agent is focused can fix it.
		if state.thinking.MsgIndex < 0 {
			state.thinking.MsgIndex = m.streamCommitIndex(agentID)
		}
		return state.thinking
	}
	msgIndex := -1
	blockIndex := 0
	if agentID == "" {
		msgIndex = m.thinkingStreamMsgIndex
		currentMsgIndex := m.currentMainAssistantMsgIndex()
		if msgIndex < 0 || (currentMsgIndex >= 0 && currentMsgIndex > msgIndex) {
			msgIndex = currentMsgIndex
			m.thinkingStreamMsgIndex = msgIndex
			m.thinkingStreamBlockIndex = 0
		}
		blockIndex = m.thinkingStreamBlockIndex
	} else {
		msgIndex = m.streamCommitIndex(agentID)
	}
	state.thinking = &Block{ID: m.nextBlockID, Type: BlockThinking, Streaming: true, AgentID: agentID, MsgIndex: msgIndex, ThinkingBlockIndex: blockIndex}
	m.nextBlockID++
	state.thinkingAppended = false
	return state.thinking
}

func (m *Model) handleStreamingAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.StreamTextEvent:
		m.touchStreamDelta(evt.AgentID)
		state := m.streamState(evt.AgentID)
		if state.assistant != nil && !state.assistant.Streaming {
			m.finalizeAgentStream(evt.AgentID)
			state = m.streamState(evt.AgentID)
		}
		if state.assistant == nil {
			m.markRequestProgressBaseline(evt.AgentID)
			assistant := &Block{ID: m.nextBlockID, Type: BlockAssistant, Streaming: true, AgentID: evt.AgentID, StartedAt: time.Now()}
			if evt.AgentID != "" {
				assistant.MsgIndex = m.streamCommitIndex(evt.AgentID)
			}
			state.assistant = assistant
			m.nextBlockID++
			state.assistantAppended = false
		}
		if !state.assistantAppended && assistantStreamContentIsPlaceholder(state.assistant.Content) {
			state.assistant.Content = ""
			state.assistant.streamContentBuilder = nil
			if assistantStreamContentIsPlaceholder(evt.Text) {
				state.assistant.InvalidateCache()
				m.storeStreamState(evt.AgentID, state)
				m.exitRenderFreeze()
				m.markStreamRenderDirty()
				effects.addFollowup(m.scheduleStreamFlush(0))
				return true, effects
			}
		}
		state.assistant.appendStreamingContent(evt.Text)
		firstVisibleAssistantDelta := !state.assistantAppended && state.assistant.syncStreamingContent()
		if !state.assistantAppended && !assistantStreamContentIsPlaceholder(state.assistant.Content) {
			m.appendViewportBlock(state.assistant)
			state.assistantAppended = true
			if m.displayState == stateForeground {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			}
		}
		if firstVisibleAssistantDelta {
			state.assistant.InvalidateCache()
			if state.assistantAppended {
				m.viewport.InvalidateBlock(state.assistant.ID)
			}
		}
		if firstVisibleAssistantDelta && m.hasDeferredStartupTranscript() {
			m.syncStartupDeferredTranscriptBlock(state.assistant)
		}
		m.storeStreamState(evt.AgentID, state)
		m.exitRenderFreeze()
		m.markStreamRenderDirty()
		effects.addFollowup(m.scheduleStreamFlush(0))
		return true, effects
	case agent.ThinkingStartedEvent:
		state := m.streamState(evt.AgentID)
		m.ensureStreamingThinkingBlock(evt.AgentID, &state)
		m.storeStreamState(evt.AgentID, state)
		return true, effects
	case agent.StreamThinkingDeltaEvent:
		m.touchStreamDelta(evt.AgentID)
		state := m.streamState(evt.AgentID)
		m.ensureStreamingThinkingBlock(evt.AgentID, &state)
		state.thinking.appendStreamingContent(evt.Text)
		firstVisibleThinkingDelta := !state.thinkingAppended && state.thinking.syncStreamingContent()
		if strings.TrimSpace(state.thinking.Content) != "" && !state.thinkingAppended {
			m.appendViewportBlock(state.thinking)
			state.thinkingAppended = true
			if m.displayState == stateForeground {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			}
		}
		if firstVisibleThinkingDelta {
			state.thinking.InvalidateCache()
			if state.thinkingAppended {
				m.viewport.InvalidateBlock(state.thinking.ID)
			}
		}
		if firstVisibleThinkingDelta && m.hasDeferredStartupTranscript() {
			m.syncStartupDeferredTranscriptBlock(state.thinking)
		}
		m.storeStreamState(evt.AgentID, state)
		m.exitRenderFreeze()
		m.markStreamRenderDirty()
		effects.addFollowup(m.scheduleStreamFlush(0))
		return true, effects
	case agent.StreamThinkingEvent:
		state := m.streamState(evt.AgentID)
		flushedThinking := false
		if strings.TrimSpace(evt.Text) != "" {
			m.ensureStreamingThinkingBlock(evt.AgentID, &state)
			if state.thinking.replaceFinalSubAgentThinkingPayload(evt.AgentID, evt.Text) {
				flushedThinking = true
			} else {
				state.thinking.appendStreamingContent(evt.Text)
				flushedThinking = state.thinking.syncStreamingContent()
			}
			if !state.thinkingAppended {
				m.appendViewportBlock(state.thinking)
				state.thinkingAppended = true
				if m.displayState == stateForeground {
					effects.addFollowup(m.requestStreamBoundaryFlush())
				}
			}
		}
		if state.thinking != nil {
			if !flushedThinking {
				if state.thinking.replaceFinalSubAgentThinkingPayload(evt.AgentID, evt.Text) {
					flushedThinking = true
				} else {
					flushedThinking = state.thinking.syncStreamingContent()
				}
			}
			state.thinking.Streaming = false
			state.thinking.InvalidateCache()
			if state.thinkingAppended {
				if flushedThinking {
					m.viewport.UpdateBlock(state.thinking.ID)
				}
				m.markBlockSettled(state.thinking)
				m.viewport.InvalidateBlock(state.thinking.ID)
			}
			if flushedThinking && m.hasDeferredStartupTranscript() {
				m.syncStartupDeferredTranscriptBlock(state.thinking)
			}
			m.setStreamRenderInvalidation(streamRenderInvalidateForce)
			// Detach the settled block so the next round of thinking starts
			// a fresh card instead of appending to an already-settled block.
			if evt.AgentID == "" {
				m.thinkingStreamBlockIndex++
			}
			state.thinking = nil
			state.thinkingAppended = false
		}
		m.storeStreamState(evt.AgentID, state)
		effects.addFollowup(m.requestStreamBoundaryFlush())
		return true, effects
	case agent.StreamRollbackEvent:
		state := m.streamState(evt.AgentID)
		if state.thinking != nil {
			if state.thinkingAppended {
				m.removeViewportBlockByID(state.thinking.ID)
			}
			state.thinking = nil
			state.thinkingAppended = false
		}
		m.removeRolledBackThinkingBlocks(evt.AgentID)
		if state.assistant != nil {
			if state.assistantAppended {
				m.removeViewportBlockByID(state.assistant.ID)
			}
			state.assistant = nil
			state.assistantAppended = false
		}
		m.storeStreamState(evt.AgentID, state)
		if strings.TrimSpace(evt.Reason) != "" {
			effects.addFollowup(m.enqueueToast(evt.Reason, "warn"))
		}
		m.setStreamRenderInvalidation(streamRenderInvalidateForce)
		effects.addFollowup(m.requestStreamBoundaryFlush())
		return true, effects
	case agent.ThinkingTranslatedEvent:
		translated := strings.TrimSpace(evt.Translated)
		if translated == "" {
			return true, effects
		}
		if evt.BlockIndex >= 0 {
			for _, b := range slices.Backward(m.viewport.blocks) {

				if b == nil || b.Type != BlockThinking || b.Streaming || b.AgentID != evt.AgentID {
					continue
				}
				if b.MsgIndex < 0 || b.ThinkingBlockIndex != evt.BlockIndex {
					continue
				}
				if evt.MessageID != "" {
					wantMsgID := fmt.Sprintf("msgidx:%d", b.MsgIndex)
					if wantMsgID != evt.MessageID {
						continue
					}
				}
				if evt.OriginalHash != "" && recovery.ThinkingTranslationOriginalHash(b.Content) != evt.OriginalHash {
					continue
				}
				if len(b.ThinkingTranslations) <= evt.BlockIndex {
					translations := make([]ThinkingTranslationView, evt.BlockIndex+1)
					copy(translations, b.ThinkingTranslations)
					b.ThinkingTranslations = translations
				}
				if existing := strings.TrimSpace(b.ThinkingTranslations[evt.BlockIndex].Content); existing == translated {
					return true, effects
				}
				b.ThinkingTranslations[evt.BlockIndex] = ThinkingTranslationView{TargetLang: strings.TrimSpace(evt.TargetLang), Content: translated}
				b.InvalidateCache()
				m.updateViewportBlock(b)
				m.markBlockSettled(b)
				return true, effects
			}
		}
		return true, effects
	default:
		return false, effects
	}
}

func (m *Model) removeRolledBackThinkingBlocks(agentID string) {
	if m == nil || m.viewport == nil {
		return
	}
	pendingMsgIndex := m.thinkingStreamMsgIndex
	if agentID == "" {
		currentMsgIndex := m.currentMainAssistantMsgIndex()
		if pendingMsgIndex < 0 && currentMsgIndex >= 0 {
			pendingMsgIndex = currentMsgIndex
		}
	}
	for _, b := range slices.Backward(m.viewport.blocks) {

		if b == nil || b.Type != BlockThinking || b.Streaming || b.AgentID != agentID {
			continue
		}
		if agentID == "" && pendingMsgIndex >= 0 && b.MsgIndex != pendingMsgIndex {
			continue
		}
		m.removeViewportBlockByID(b.ID)
	}
	if agentID == "" {
		m.thinkingStreamMsgIndex = pendingMsgIndex
		m.thinkingStreamBlockIndex = 0
	}
}
