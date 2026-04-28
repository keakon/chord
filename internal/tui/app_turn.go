package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func (m *Model) finalizeAssistantBlock() {
	if m.currentThinkingBlock != nil {
		m.currentThinkingBlock.Streaming = false
		// Only set ThinkingDuration here if it wasn't already frozen by
		// StreamThinkingEvent (e.g. when thinking_end was never received
		// due to cancellation or provider interleaving).
		if !m.thinkingStartTime.IsZero() {
			m.currentThinkingBlock.ThinkingDuration = time.Since(m.thinkingStartTime)
			m.thinkingStartTime = time.Time{}
		}
		if m.thinkingBlockAppended {
			if m.currentThinkingBlock.SettledAt.IsZero() {
				m.markBlockSettled(m.currentThinkingBlock)
			}
			m.currentThinkingBlock.InvalidateCache()
			m.viewport.UpdateLastBlock()
			m.syncStartupDeferredTranscriptBlock(m.currentThinkingBlock)
		}
		m.currentThinkingBlock = nil
		m.thinkingBlockAppended = false
	}
	if m.currentAssistantBlock != nil {
		m.currentAssistantBlock.Streaming = false
		// Heuristic: discard very-short streaming assistant prefix if it's
		// likely an orphan fragment before a tool call (e.g. "Okay," "Sure", "Let").
		// Only applies when the block was appended and the previous block is a
		// completed tool call.
		shouldDiscard := false
		if m.assistantBlockAppended && m.currentAssistantBlock.Type == BlockAssistant {
			content := removeTrailingCursorGlyph(m.currentAssistantBlock.Content)
			nonWS := stripANSI(strings.TrimSpace(content))
			// Check if content is 1-3 chars or a single short word (<=5 chars, letters only)
			isVeryShort := len(nonWS) <= 3 || (len(nonWS) <= 5 && isASCIILettersOnly(nonWS))
			if isVeryShort {
				// Check if previous visible block is a completed tool call
				blocks := m.viewport.visibleBlocks()
				for i, b := range blocks {
					if b == m.currentAssistantBlock {
						if i > 0 {
							prev := blocks[i-1]
							if prev.Type == BlockToolCall && prev.ResultDone {
								shouldDiscard = true
							}
						}
						break
					}
				}
			}
		}
		if shouldDiscard {
			m.removeViewportBlockByID(m.currentAssistantBlock.ID)
			m.currentAssistantBlock = nil
			m.assistantBlockAppended = false
			return
		}
		m.currentAssistantBlock.InvalidateCache()
		if m.assistantBlockAppended {
			m.markBlockSettled(m.currentAssistantBlock)
		}
		m.viewport.UpdateLastBlock()
		m.syncStartupDeferredTranscriptBlock(m.currentAssistantBlock)
		m.currentAssistantBlock = nil
		m.assistantBlockAppended = false
	}
}

// isASCIILettersOnly returns true if s contains only ASCII letters (a-zA-Z).
func isASCIILettersOnly(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

func (m *Model) finalizeTurn() {
	m.finalizeAssistantBlock()
	m.flushPendingLocalStatusCards()
}

func (m *Model) sendDraft(draft queuedDraft) tea.Cmd {
	_, imageCount := queuedDraftTextAndImageCount(draft)
	msgIndex := -1
	if m.focusedAgentID == "" && m.agent != nil {
		msgIndex = len(m.agent.GetMessages())
	}
	// The composer was just Reset() upstream; recompute viewport height before
	// AppendBlock so scrollToEnd uses the new, larger viewport and the newly
	// appended USER block is not pushed below the visible window — otherwise
	// the card's trailing rows (padBottom / marginBottom, and sometimes body
	// lines when the previous composer was multi-line) get clipped.
	m.recalcViewportSize()
	block := &Block{
		ID:         m.nextBlockID,
		Type:       BlockUser,
		Content:    userBlockTextFromParts(draft.contentParts(), draft.Content),
		AgentID:    m.focusedAgentID,
		LoopAnchor: draft.LoopAnchor,
		ImageCount: imageCount,
		ImageParts: imagePartsFromContentParts(draft.contentParts()),
		FileRefs:   draft.FileRefs,
		MsgIndex:   msgIndex,
		StartedAt:  draft.QueuedAt,
	}
	m.nextBlockID++
	m.appendViewportBlock(block)
	m.markBlockSettled(block)
	d := draft
	if strings.TrimSpace(d.AgentID) == "" {
		d.AgentID = m.focusedAgentID
	}
	m.inflightDraft = &d
	// Update terminal title from the first user message.
	if m.terminalTitleBase == "" {
		content := draft.Content
		if len(draft.Parts) > 0 && draft.Parts[0].Type == "text" {
			content = draft.Parts[0].Text
		}
		m.setTerminalTitleFromMessage(content)
	}
	if m.agent != nil {
		if len(draft.Parts) > 0 {
			m.agent.SendUserMessageWithParts(draft.Parts)
		} else {
			m.agent.SendUserMessage(draft.Content)
		}
	}
	m.syncVisibleMainUserBlockMsgIndexes()
	return tea.Batch(
		m.imageProtocolCmd(),
		m.hostRedrawForContentBoundaryCmd("live-append"),
	)
}

func (m *Model) drainQueuedDrafts() tea.Cmd {
	if len(m.queuedDrafts) == 0 {
		return nil
	}
	draft := m.queuedDrafts[0]
	m.queuedDrafts = m.queuedDrafts[1:]
	if draft.QueuedAt.IsZero() {
		draft.QueuedAt = time.Now()
	}
	m.finalizeTurn()
	protoCmd := m.sendDraft(draft)
	m.recalcViewportSize()
	return tea.Batch(m.startAnimTick(), protoCmd)
}

func (m *Model) resetStreamingToIdle() {
	m.finalizeTurn()
	m.markAgentIdle("main")
	m.stopActiveAnimationIfIdle()
}

// touchStreamDelta records that a visible streaming delta was received for the
// given agent. streamingStale uses this timestamp to distinguish "long-running
// but active streaming" from "connection silently lost".
func (m *Model) touchStreamDelta(agentID string) {
	if agentID == "" {
		agentID = "main"
	}
	m.streamLastDeltaAt[agentID] = time.Now()
}

func (m Model) inflightDraftBelongsToAgent(agentID string) bool {
	if m.inflightDraft == nil {
		return false
	}
	return normalizeDraftAgentID(m.inflightDraft.AgentID) == normalizeDraftAgentID(agentID)
}

func (m *Model) streamingStale() bool {
	aid := m.focusedAgentID
	if aid == "" {
		aid = "main"
	}
	a, ok := m.activities[aid]
	if !ok || a.Type != agent.ActivityStreaming {
		return false
	}
	lastDelta, ok := m.streamLastDeltaAt[aid]
	if !ok || lastDelta.IsZero() {
		start, ok := m.activityStartTime[aid]
		if !ok || start.IsZero() {
			return false
		}
		return time.Since(start) > 5*time.Minute
	}
	return time.Since(lastDelta) > 5*time.Minute
}

func (m *Model) isAgentBusy() bool {
	return m.focusedAgentBusyForIdleSweep()
}

func (m *Model) focusedAgentIDOrMain() string {
	if m.focusedAgentID == "" {
		return "main"
	}
	return m.focusedAgentID
}

func (m *Model) focusedAgentHasRuntimeActivity() bool {
	aid := m.focusedAgentIDOrMain()
	a, ok := m.activities[aid]
	if !ok {
		return false
	}
	switch a.Type {
	case agent.ActivityIdle:
		return false
	case agent.ActivityCooling, agent.ActivityCompacting,
		agent.ActivityConnecting, agent.ActivityWaitingHeaders, agent.ActivityWaitingToken,
		agent.ActivityStreaming, agent.ActivityExecuting, agent.ActivityRetrying, agent.ActivityRetryingKey:
		return true
	default:
		return false
	}
}

func (m *Model) focusedAgentBusyForIdleSweep() bool {
	if m.inflightDraftBelongsToAgent(m.focusedAgentIDOrMain()) {
		return true
	}
	if m.mode == ModeConfirm && m.confirm.request != nil {
		return true
	}
	if m.mode == ModeQuestion && m.question.request != nil {
		return true
	}
	if m.viewport != nil {
		if m.viewport.HasUserLocalShellPending() || m.viewport.HasPendingToolWork() {
			return true
		}
	}
	return m.focusedAgentHasRuntimeActivity()
}

func (m *Model) tryContinue() tea.Cmd {
	if m.agent == nil || m.continueBlocked() {
		return nil
	}
	msgs := m.agent.GetMessages()
	if len(msgs) == 0 {
		return nil
	}
	last := msgs[len(msgs)-1]
	switch last.Role {
	case "user", "tool":
		m.agent.ContinueFromContext()
		return m.startAnimTick()
	case "assistant":
		if len(last.ToolCalls) > 0 {
			m.agent.ContinueFromContext()
			return m.startAnimTick()
		}
		if len(last.ThinkingBlocks) > 0 && strings.TrimSpace(last.Content) == "" {
			m.agent.RemoveLastMessage()
			m.agent.ContinueFromContext()
			return m.startAnimTick()
		}
		if last.StopReason == "stop" || last.StopReason == "end_turn" {
			draft := queuedDraft{Content: "continue"}
			m.finalizeTurn()
			return tea.Batch(m.startAnimTick(), m.sendDraft(draft))
		}
		m.agent.ContinueFromContext()
		return m.startAnimTick()
	}
	return nil
}

func (m *Model) continueBlocked() bool {
	if m == nil {
		return true
	}
	if m.inflightDraftBelongsToAgent(m.focusedAgentIDOrMain()) {
		return true
	}
	if m.mode == ModeConfirm && m.confirm.request != nil {
		return true
	}
	if m.mode == ModeQuestion && m.question.request != nil {
		return true
	}
	if m.viewport != nil && m.viewport.HasUserLocalShellPending() {
		return true
	}
	return m.focusedAgentHasRuntimeActivity()
}

func (m *Model) loadLastUserMessageToComposer() tea.Cmd {
	if m.agent == nil {
		return nil
	}
	msgs := m.agent.GetMessages()
	var last *message.Message
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			last = &msgs[i]
			break
		}
	}
	if last == nil {
		return nil
	}
	var parts []message.ContentPart
	if len(last.Parts) > 0 {
		parts = last.Parts
	} else if last.Content != "" {
		parts = []message.ContentPart{{Type: "text", Text: last.Content}}
	}
	if len(parts) == 0 {
		return nil
	}
	m.editingQueuedDraftID = ""
	text, inlinePastes := displayTextAndInlinePastes(parts, "")
	nextPasteSeq := 0
	for _, paste := range inlinePastes {
		if paste.Seq > nextPasteSeq {
			nextPasteSeq = paste.Seq
		}
	}
	if text == "" {
		draft := queuedDraftFromParts(parts)
		text, _ = queuedDraftTextAndImageCount(draft)
	}
	m.input.SetDisplayValueAndPastes(text, inlinePastes, nextPasteSeq)
	m.input.syncHeight()
	m.attachments = attachmentsFromParts(parts)
	m.recalcViewportSize()
	return nil
}
