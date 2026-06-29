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
	content = visibleAssistantStreamContent(content)
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

func (m *Model) ensureStreamingThinkingBlock(agentID string) *Block {
	if m.currentThinkingBlock != nil {
		return m.currentThinkingBlock
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
	m.currentThinkingBlock = &Block{ID: m.nextBlockID, Type: BlockThinking, Streaming: true, AgentID: agentID, MsgIndex: msgIndex, ThinkingBlockIndex: blockIndex}
	m.nextBlockID++
	m.thinkingBlockAppended = false
	return m.currentThinkingBlock
}

func (m *Model) handleStreamingAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.StreamTextEvent:
		m.touchStreamDelta(evt.AgentID)
		if m.currentAssistantBlock != nil && (m.currentAssistantBlock.AgentID != evt.AgentID || !m.currentAssistantBlock.Streaming) {
			m.finalizeAssistantBlock()
			m.currentAssistantBlock = nil
			m.assistantBlockAppended = false
		}
		if m.currentAssistantBlock == nil {
			m.markRequestProgressBaseline(evt.AgentID)
			m.currentAssistantBlock = &Block{ID: m.nextBlockID, Type: BlockAssistant, Streaming: true, AgentID: evt.AgentID, StartedAt: time.Now()}
			m.nextBlockID++
			m.assistantBlockAppended = false
		}
		if !m.assistantBlockAppended && assistantStreamContentIsPlaceholder(m.currentAssistantBlock.Content) {
			m.currentAssistantBlock.Content = ""
			m.currentAssistantBlock.streamContentBuilder = nil
			if assistantStreamContentIsPlaceholder(evt.Text) {
				m.currentAssistantBlock.InvalidateCache()
				m.exitRenderFreeze()
				m.markStreamRenderDirty()
				effects.addFollowup(m.scheduleStreamFlush(0))
				return true, effects
			}
		}
		m.currentAssistantBlock.appendStreamingContent(evt.Text)
		firstVisibleAssistantDelta := !m.assistantBlockAppended && m.currentAssistantBlock.syncStreamingContent()
		if !m.assistantBlockAppended && !assistantStreamContentIsPlaceholder(m.currentAssistantBlock.Content) {
			m.appendViewportBlock(m.currentAssistantBlock)
			m.assistantBlockAppended = true
			if m.displayState == stateForeground {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			}
		}
		if firstVisibleAssistantDelta {
			m.currentAssistantBlock.InvalidateCache()
			if m.assistantBlockAppended {
				m.viewport.InvalidateBlock(m.currentAssistantBlock.ID)
			}
		}
		if firstVisibleAssistantDelta && m.hasDeferredStartupTranscript() {
			m.syncStartupDeferredTranscriptBlock(m.currentAssistantBlock)
		}
		m.exitRenderFreeze()
		m.markStreamRenderDirty()
		effects.addFollowup(m.scheduleStreamFlush(0))
		return true, effects
	case agent.ThinkingStartedEvent:
		if m.thinkingStartTime.IsZero() {
			m.thinkingStartTime = time.Now()
		}
		m.ensureStreamingThinkingBlock("")
		return true, effects
	case agent.StreamThinkingDeltaEvent:
		m.touchStreamDelta(evt.AgentID)
		if m.thinkingStartTime.IsZero() {
			m.thinkingStartTime = time.Now()
		}
		if m.currentThinkingBlock != nil && m.currentThinkingBlock.AgentID != evt.AgentID {
			m.finalizeAssistantBlock()
		}
		m.ensureStreamingThinkingBlock(evt.AgentID)
		m.currentThinkingBlock.appendStreamingContent(evt.Text)
		firstVisibleThinkingDelta := !m.thinkingBlockAppended && m.currentThinkingBlock.syncStreamingContent()
		if strings.TrimSpace(m.currentThinkingBlock.Content) != "" && !m.thinkingBlockAppended {
			m.appendViewportBlock(m.currentThinkingBlock)
			m.thinkingBlockAppended = true
			if m.displayState == stateForeground {
				effects.addFollowup(m.requestStreamBoundaryFlush())
			}
		}
		if firstVisibleThinkingDelta {
			m.currentThinkingBlock.InvalidateCache()
			if m.thinkingBlockAppended {
				m.viewport.InvalidateBlock(m.currentThinkingBlock.ID)
			}
		}
		if firstVisibleThinkingDelta && m.hasDeferredStartupTranscript() {
			m.syncStartupDeferredTranscriptBlock(m.currentThinkingBlock)
		}
		m.exitRenderFreeze()
		m.markStreamRenderDirty()
		effects.addFollowup(m.scheduleStreamFlush(0))
		return true, effects
	case agent.StreamThinkingEvent:
		flushedThinking := false
		if strings.TrimSpace(evt.Text) != "" {
			m.ensureStreamingThinkingBlock(evt.AgentID)
			m.currentThinkingBlock.appendStreamingContent(evt.Text)
			flushedThinking = m.currentThinkingBlock.syncStreamingContent()
			if !m.thinkingBlockAppended {
				m.appendViewportBlock(m.currentThinkingBlock)
				m.thinkingBlockAppended = true
				if m.displayState == stateForeground {
					effects.addFollowup(m.requestStreamBoundaryFlush())
				}
			}
		}
		if m.currentThinkingBlock != nil {
			if !flushedThinking {
				flushedThinking = m.currentThinkingBlock.syncStreamingContent()
			}
			m.currentThinkingBlock.Streaming = false
			if !m.thinkingStartTime.IsZero() {
				m.currentThinkingBlock.ThinkingDuration = time.Since(m.thinkingStartTime)
				m.thinkingStartTime = time.Time{}
			}
			m.currentThinkingBlock.InvalidateCache()
			if m.thinkingBlockAppended {
				if flushedThinking {
					m.viewport.UpdateBlock(m.currentThinkingBlock.ID)
				}
				m.markBlockSettled(m.currentThinkingBlock)
				m.viewport.InvalidateBlock(m.currentThinkingBlock.ID)
			}
			if flushedThinking && m.hasDeferredStartupTranscript() {
				m.syncStartupDeferredTranscriptBlock(m.currentThinkingBlock)
			}
			m.setStreamRenderInvalidation(streamRenderInvalidateForce)
			// Detach the settled block so the next round of thinking starts
			// a fresh card. Without this, subsequent thinking deltas would
			// be appended to an already-frozen block and the footer would
			// render alongside still-streaming content.
			if m.currentThinkingBlock.AgentID == "" {
				m.thinkingStreamBlockIndex++
			}
			m.currentThinkingBlock = nil
			m.thinkingBlockAppended = false
		}
		effects.addFollowup(m.requestStreamBoundaryFlush())
		return true, effects
	case agent.StreamRollbackEvent:
		matchAgent := func(blockAgent string) bool { return blockAgent == evt.AgentID }
		if m.currentThinkingBlock != nil && matchAgent(m.currentThinkingBlock.AgentID) {
			if m.thinkingBlockAppended {
				m.removeViewportBlockByID(m.currentThinkingBlock.ID)
			}
			m.currentThinkingBlock = nil
			m.thinkingBlockAppended = false
			m.thinkingStartTime = time.Time{}
		}
		m.removeRolledBackThinkingBlocks(evt.AgentID)
		if m.currentAssistantBlock != nil && matchAgent(m.currentAssistantBlock.AgentID) {
			if m.assistantBlockAppended {
				m.removeViewportBlockByID(m.currentAssistantBlock.ID)
			}
			m.currentAssistantBlock = nil
			m.assistantBlockAppended = false
		}
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
