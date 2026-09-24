package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/golog/log"

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

// markStreamSegmentSettled records the newest segment whose card has stopped
// streaming for agentID, so a delta arriving after the card is gone can be
// routed back to that segment's card instead of opening a second card holding
// the tail of the same reply. Only the newest segment per agent is kept: a newer
// segment subsumes older ones, and an out-of-order older report is ignored.
func (m *Model) markStreamSegmentSettled(agentID string, id streamSegmentIdentity) {
	if m == nil || !id.known() {
		return
	}
	if prev, ok := m.settledStreamSegments[agentID]; ok && !id.after(prev) {
		return
	}
	if m.settledStreamSegments == nil {
		m.settledStreamSegments = make(map[string]streamSegmentIdentity)
	}
	m.settledStreamSegments[agentID] = id
}

// streamSegmentSettled reports whether id no longer owns a live card for
// agentID: its producer has reported the segment end, or a scheduling boundary
// settled the card it produced. An unknown identity is never settled — paths
// and tests that never tagged their deltas keep opening cards as before.
func (m *Model) streamSegmentSettled(agentID string, id streamSegmentIdentity) bool {
	if m == nil || !id.known() {
		return false
	}
	settled, ok := m.settledStreamSegments[agentID]
	return ok && settled.covers(id)
}

// streamCardForSegment returns the last viewport card that the segment
// identified by id produced for agentID, streaming or settled, or nil when no
// loaded card holds that segment. It is how a late event finds the card of its
// own segment: the card carries the identity, not the stream state, so the
// lookup still works after the card settled and detached.
func (m *Model) streamCardForSegment(agentID string, blockType BlockType, id streamSegmentIdentity) *Block {
	if m == nil || m.viewport == nil || !id.known() {
		return nil
	}
	for _, b := range slices.Backward(m.viewport.blocks) {
		if b == nil || b.Type != blockType || b.AgentID != agentID || b.StreamTurnID != id.turnID || b.StreamRequestSeq != id.requestSeq {
			continue
		}
		return b
	}
	return nil
}

// mergeStaleStreamTail folds a delta of a segment that no longer owns the
// agent's live card back into the card that segment produced. It is the fallback
// behind the producer's segment end — for a tail delivered after its card
// settled (reordered delivery, another settle path). Only a settled, loaded card
// with the same (agent, turn, request) identity is eligible. It reports whether
// the text was absorbed; a missing card, a card still streaming, or one cold in
// spill storage means the caller drops the delta: the cancel snapshot already
// persisted the text up to the interrupt, and only the sub-20ms batch flushed
// afterwards can be missing, so a card of its own would render the tail twice.
//
// A transcript rebuild drops the cards and clears the settled record, so a delta
// that lands after one finds no card here and opens its own: the rebuild can
// happen while a producer is still streaming, and dropping its remaining text
// would be worse than showing it in a second card.
func (m *Model) mergeStaleStreamTail(agentID string, blockType BlockType, id streamSegmentIdentity, text string) bool {
	if text == "" {
		return false
	}
	b := m.streamCardForSegment(agentID, blockType, id)
	if b == nil {
		log.Debugf("tui: dropped late delta type=%d of segment agent=%q turn=%d request=%d: no card holds that segment", blockType, agentID, id.turnID, id.requestSeq)
		return false
	}
	if b.Streaming || b.spillCold {
		log.Debugf("tui: dropped late delta type=%d of segment agent=%q turn=%d request=%d: card is still streaming or spilled", blockType, agentID, id.turnID, id.requestSeq)
		return false
	}
	b.appendStreamingContent(text)
	b.syncStreamingContent()
	b.InvalidateCache()
	m.updateViewportBlock(b)
	return true
}

// absorbLateStreamTail consumes a delta that must not open a card of its own —
// an older segment's tail arriving behind a newer stream, or a settled segment's
// tail with no live card — and asks for the render flush that shows whatever the
// merge absorbed.
func (m *Model) absorbLateStreamTail(agentID string, blockType BlockType, id streamSegmentIdentity, text string) agentEventEffects {
	m.mergeStaleStreamTail(agentID, blockType, id, text)
	m.exitRenderFreeze()
	m.markStreamRenderDirty()
	var effects agentEventEffects
	effects.addFollowup(m.scheduleStreamFlush(0))
	return effects
}

func blockStreamSegmentIdentity(b *Block) streamSegmentIdentity {
	if b == nil {
		return streamSegmentIdentity{}
	}
	return streamSegmentIdentity{turnID: b.StreamTurnID, requestSeq: b.StreamRequestSeq}
}

// adoptStreamSegmentIdentity stamps an untagged streaming card with the segment
// identity of the delta that first names it.
func adoptStreamSegmentIdentity(b *Block, id streamSegmentIdentity) {
	if b == nil || !id.known() || blockStreamSegmentIdentity(b).known() {
		return
	}
	b.StreamTurnID = id.turnID
	b.StreamRequestSeq = id.requestSeq
}

// streamSegmentEnded reports whether the producer segment owning b has reported
// its end. Cards that are not streaming and cards without identity are trivially
// done: neither is waiting on a producer.
func (m *Model) streamSegmentEnded(agentID string, b *Block) bool {
	if b == nil || !b.Streaming {
		return true
	}
	id := blockStreamSegmentIdentity(b)
	if !id.known() {
		return true
	}
	ended, ok := m.streamEndedSegments[agentID]
	return ok && ended.covers(id)
}

// streamSegmentPending reports whether agentID still has a streaming card whose
// producer has not reported the end of its segment. Terminal events that only
// mean "this agent is scheduling-idle now" (IdleEvent, GlobalIdleEvent, a
// SubAgent ActivityIdle) must not settle such a card: the cancelled request
// flushes its last text batch after them, and settling first would render that
// batch as a second card holding the tail of the same reply.
func (m *Model) streamSegmentPending(agentID string) bool {
	state := m.streamState(agentID)
	return !m.streamSegmentEnded(agentID, state.assistant) || !m.streamSegmentEnded(agentID, state.thinking)
}

// finalizeAgentStreamForIdleEvent settles agentID's streaming cards at a
// scheduling idle boundary, unless a producer segment still owns them.
func (m *Model) finalizeAgentStreamForIdleEvent(agentID string) {
	if m.streamSegmentPending(agentID) {
		return
	}
	m.finalizeAgentStream(agentID)
}

// finalizeTurnForIdleEvent is finalizeAgentStreamForIdleEvent at the main
// agent's turn boundary.
func (m *Model) finalizeTurnForIdleEvent() {
	m.finalizeAgentStreamForIdleEvent("")
}

// markStreamSegmentEnded records the newest producer segment that reported its
// end for agentID, so streamSegmentPending can tell a card whose producer is
// done from one that may still flush text.
func (m *Model) markStreamSegmentEnded(agentID string, id streamSegmentIdentity) {
	if m == nil || !id.known() {
		return
	}
	if prev, ok := m.streamEndedSegments[agentID]; ok && !id.after(prev) {
		return
	}
	if m.streamEndedSegments == nil {
		m.streamEndedSegments = make(map[string]streamSegmentIdentity)
	}
	m.streamEndedSegments[agentID] = id
	// The producer reported this segment's stream finished, so whatever card it
	// produced is done streaming: record it for late-delta attribution too.
	m.markStreamSegmentSettled(agentID, id)
}

func (m *Model) handleStreamingAgentEvent(event agent.AgentEvent) (bool, agentEventEffects) {
	var effects agentEventEffects
	switch evt := event.(type) {
	case agent.StreamTextEvent:
		m.touchStreamDelta(evt.AgentID)
		state := m.streamState(evt.AgentID)
		incoming := streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq}
		if state.assistant != nil {
			current := blockStreamSegmentIdentity(state.assistant)
			switch {
			case current == incoming:
				// Same segment (or both untagged): continue in this card. A
				// settled card still referenced here absorbs its own late tail
				// below instead of splitting a second card.
			case state.assistant.Streaming && incoming.after(current):
				// A newer segment is producing text for this agent, so the card
				// belongs to a segment that stopped producing: settle it and
				// open a new one rather than appending two requests' text into
				// one card.
				m.finalizeAgentStream(evt.AgentID)
				state = m.streamState(evt.AgentID)
			case state.assistant.Streaming:
				// An older segment's tail arriving while a newer one streams:
				// merge it back into its own card, or drop it, so it cannot
				// pollute the live card.
				effects = m.absorbLateStreamTail(evt.AgentID, BlockAssistant, incoming, evt.Text)
				return true, effects
			default:
				// The settled card still referenced here belongs to another
				// segment: release it and fall through, where the
				// settled-segment check decides whether the delta merges into
				// its own card or is dropped.
				state.assistant = nil
				state.assistantAppended = false
			}
		}
		if state.assistant == nil {
			if m.streamSegmentSettled(evt.AgentID, incoming) {
				// The card that segment produced already stopped streaming
				// (usually an ESC cancel whose final batch lands after it
				// settled). Merge into that card when it is still around,
				// otherwise drop: a fresh card would split the tail of the same
				// reply in two. A newer request of the same turn is not settled,
				// so it still opens a card of its own below.
				effects = m.absorbLateStreamTail(evt.AgentID, BlockAssistant, incoming, evt.Text)
				return true, effects
			}
			m.markRequestProgressBaseline(evt.AgentID)
			assistant := &Block{ID: m.nextBlockID, Type: BlockAssistant, Streaming: true, AgentID: evt.AgentID, StartedAt: time.Now(), StreamTurnID: incoming.turnID, StreamRequestSeq: incoming.requestSeq}
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
	case agent.StreamTextCommitEvent:
		return true, m.commitStreamText(evt)
	case agent.StreamSegmentEndedEvent:
		// The producer of (agent, turn, request) flushed its last delta and will
		// emit no more text for it, so its streaming card may settle now —
		// this is the authority; a scheduling idle signal must not settle it
		// first (see streamSegmentPending).
		id := streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq}
		m.markStreamSegmentEnded(evt.AgentID, id)
		state := m.streamState(evt.AgentID)
		if m.streamSegmentEnded(evt.AgentID, state.assistant) && m.streamSegmentEnded(evt.AgentID, state.thinking) &&
			((state.assistant != nil && state.assistant.Streaming) || (state.thinking != nil && state.thinking.Streaming)) {
			m.finalizeAgentStream(evt.AgentID)
		}
		return true, effects
	case agent.ThinkingStartedEvent:
		state := m.streamState(evt.AgentID)
		m.ensureStreamingThinkingBlock(evt.AgentID, &state)
		adoptStreamSegmentIdentity(state.thinking, streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq})
		m.storeStreamState(evt.AgentID, state)
		return true, effects
	case agent.StreamThinkingDeltaEvent:
		m.touchStreamDelta(evt.AgentID)
		state := m.streamState(evt.AgentID)
		incoming := streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq}
		if state.thinking != nil {
			current := blockStreamSegmentIdentity(state.thinking)
			switch {
			case current == incoming:
				// Same segment (or both untagged): continue in this card.
			case state.thinking.Streaming && incoming.after(current):
				// A newer segment is producing thinking for this agent, so this
				// card belongs to a segment that stopped producing: settle it
				// and open a new one rather than appending two requests'
				// reasoning into one card.
				m.finalizeAgentStream(evt.AgentID)
				state = m.streamState(evt.AgentID)
			case state.thinking.Streaming:
				// An older segment's thinking tail arriving behind a newer one.
				effects = m.absorbLateStreamTail(evt.AgentID, BlockThinking, incoming, evt.Text)
				return true, effects
			default:
				// The settled card still referenced here belongs to another
				// segment: release it and fall through.
				state.thinking = nil
				state.thinkingAppended = false
			}
		}
		if state.thinking == nil {
			if m.streamSegmentSettled(evt.AgentID, incoming) {
				// A thinking tail of a segment whose card already stopped
				// streaming must not open a second thinking card.
				effects = m.absorbLateStreamTail(evt.AgentID, BlockThinking, incoming, evt.Text)
				return true, effects
			}
			m.ensureStreamingThinkingBlock(evt.AgentID, &state)
		}
		adoptStreamSegmentIdentity(state.thinking, incoming)
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
		incoming := streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq}
		if state.thinking != nil {
			current := blockStreamSegmentIdentity(state.thinking)
			if incoming.known() && current.known() && current.after(incoming) {
				// An older segment's thinking_end must not settle the card a
				// newer segment is still streaming into: its own card already
				// holds what this payload repeats (main-agent payloads are
				// empty), and settling the live card would split the newer
				// segment's reasoning into a second card.
				log.Debugf("tui: dropped late thinking_end of older segment agent=%q turn=%d request=%d (live turn=%d request=%d)",
					evt.AgentID, incoming.turnID, incoming.requestSeq, current.turnID, current.requestSeq)
				return true, effects
			}
		}
		if state.thinking == nil {
			if m.streamSegmentSettled(evt.AgentID, incoming) {
				// A thinking_end landing after its segment's card settled must not
				// open a second thinking card: the settled card already shows what
				// this segment's deltas streamed, and the payload is only the
				// authoritative copy of that same block. Drop it whenever that
				// card still exists and is no longer streaming — including when
				// it has been spilled cold. A late delta of the same segment
				// already drops on a cold card (mergeStaleStreamTail); opening a
				// new thinking card here would render the same block twice.
				if b := m.streamCardForSegment(evt.AgentID, BlockThinking, incoming); b != nil && !b.Streaming {
					log.Debugf("tui: dropped late thinking_end of settled segment agent=%q turn=%d request=%d", evt.AgentID, incoming.turnID, incoming.requestSeq)
					return true, effects
				}
			}
		}
		flushedThinking := false
		if strings.TrimSpace(evt.Text) != "" {
			m.ensureStreamingThinkingBlock(evt.AgentID, &state)
			adoptStreamSegmentIdentity(state.thinking, streamSegmentIdentity{turnID: evt.TurnID, requestSeq: evt.RequestSeq})
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
