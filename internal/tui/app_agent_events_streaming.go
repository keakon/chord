package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/keakon/chord/internal/agent"
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

func visibleAssistantStreamContent(content string) string {
	content = removeTrailingCursorGlyph(content)
	content = stripANSI(strings.TrimSpace(content))
	return strings.TrimSpace(content)
}

func assistantStreamContentIsPlaceholder(content string) bool {
	content = strings.TrimSpace(removeTrailingCursorGlyph(content))
	if strings.ContainsRune(content, '\x1b') {
		content = strings.TrimSpace(stripANSI(content))
	}
	if content == "" {
		return true
	}
	dots := 0
	hasEllipsis := false
	for _, r := range content {
		switch {
		case r == '.':
			dots++
		case r == '…':
			hasEllipsis = true
		case unicode.IsSpace(r):
		default:
			return false
		}
	}
	return hasEllipsis || dots > 0
}

func (m *Model) currentMainAssistantMsgIndex() int {
	if m == nil || m.agent == nil {
		return -1
	}
	return len(m.agent.GetMessages())
}

func (m *Model) ensureStreamingThinkingBlock(agentID string, state *agentStreamState) *Block {
	if state.thinking != nil {
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
			state.assistant = &Block{ID: m.nextBlockID, Type: BlockAssistant, Streaming: true, AgentID: evt.AgentID, StartedAt: time.Now()}
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
		if state.thinkingStartedAt.IsZero() {
			state.thinkingStartedAt = time.Now()
		}
		m.ensureStreamingThinkingBlock(evt.AgentID, &state)
		m.storeStreamState(evt.AgentID, state)
		return true, effects
	case agent.StreamThinkingDeltaEvent:
		m.touchStreamDelta(evt.AgentID)
		state := m.streamState(evt.AgentID)
		if state.thinkingStartedAt.IsZero() {
			state.thinkingStartedAt = time.Now()
		}
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
			state.thinking.appendStreamingContent(evt.Text)
			flushedThinking = state.thinking.syncStreamingContent()
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
				flushedThinking = state.thinking.syncStreamingContent()
			}
			state.thinking.Streaming = false
			if !state.thinkingStartedAt.IsZero() {
				state.thinking.ThinkingDuration = time.Since(state.thinkingStartedAt)
				state.thinkingStartedAt = time.Time{}
			}
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
			// a fresh card. Without this, subsequent thinking deltas would
			// be appended to an already-frozen block and the footer would
			// render alongside still-streaming content.
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
			state.thinkingStartedAt = time.Time{}
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
			for i := len(m.viewport.blocks) - 1; i >= 0; i-- {
				b := m.viewport.blocks[i]
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
	for i := len(m.viewport.blocks) - 1; i >= 0; i-- {
		b := m.viewport.blocks[i]
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
