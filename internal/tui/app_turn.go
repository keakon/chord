package tui

import (
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
)

type focusedContinueAction uint8

const (
	focusedContinueFromContext focusedContinueAction = iota + 1
	focusedContinueAfterRemovingLast
	focusedContinueWithDraft
)

type focusedContinueActionMsg struct {
	action focusedContinueAction
	target agent.ConversationTarget
}

type agentStreamState struct {
	assistant         *Block
	assistantAppended bool
	thinking          *Block
	thinkingAppended  bool
	thinkingStartedAt time.Time
}

func (m *Model) streamState(agentID string) agentStreamState {
	if agentID == "" {
		return agentStreamState{
			assistant:         m.currentAssistantBlock,
			assistantAppended: m.assistantBlockAppended,
			thinking:          m.currentThinkingBlock,
			thinkingAppended:  m.thinkingBlockAppended,
			thinkingStartedAt: m.thinkingStartTime,
		}
	}
	if m.subAgentStreamStates == nil {
		return agentStreamState{}
	}
	return m.subAgentStreamStates[agentID]
}

func (m *Model) hasActiveStreamBlock() bool {
	if m.currentAssistantBlock != nil || m.currentThinkingBlock != nil {
		return true
	}
	for _, state := range m.subAgentStreamStates {
		if state.assistant != nil || state.thinking != nil {
			return true
		}
	}
	return false
}

func (m *Model) storeStreamState(agentID string, state agentStreamState) {
	if agentID == "" {
		m.currentAssistantBlock = state.assistant
		m.assistantBlockAppended = state.assistantAppended
		m.currentThinkingBlock = state.thinking
		m.thinkingBlockAppended = state.thinkingAppended
		m.thinkingStartTime = state.thinkingStartedAt
		return
	}
	if state.assistant == nil && state.thinking == nil {
		delete(m.subAgentStreamStates, agentID)
		return
	}
	if m.subAgentStreamStates == nil {
		m.subAgentStreamStates = make(map[string]agentStreamState)
	}
	m.subAgentStreamStates[agentID] = state
}

func (m *Model) finalizeAssistantBlock() {
	m.finalizeAgentStream("")
	for agentID := range m.subAgentStreamStates {
		m.finalizeAgentStream(agentID)
	}
	m.flushPendingLocalStatusCards()
}

func (m *Model) finalizeAgentStream(agentID string) {
	state := m.streamState(agentID)
	if state.thinking != nil {
		state.thinking.finishStreamingContent()
		state.thinking.Streaming = false
		// Only set ThinkingDuration here if it wasn't already frozen by
		// StreamThinkingEvent (e.g. when thinking_end was never received
		// due to cancellation or provider interleaving).
		if !state.thinkingStartedAt.IsZero() {
			state.thinking.ThinkingDuration = time.Since(state.thinkingStartedAt)
			state.thinkingStartedAt = time.Time{}
		}
		if state.thinkingAppended {
			if state.thinking.SettledAt.IsZero() {
				m.markBlockSettled(state.thinking)
			}
			state.thinking.InvalidateCache()
			m.viewport.UpdateBlock(state.thinking.ID)
			m.syncStartupDeferredTranscriptBlock(state.thinking)
		}
		if agentID == "" {
			m.thinkingStreamBlockIndex++
		}
		state.thinking = nil
		state.thinkingAppended = false
	}
	if state.assistant != nil {
		state.assistant.finishStreamingContent()
		state.assistant.Streaming = false
		// Heuristic: discard very-short streaming assistant prefix if it's
		// likely an orphan fragment before a tool call (e.g. "Okay," "Sure", "Let").
		// Only applies when the block was appended and the previous block is a
		// completed tool call.
		shouldDiscard := false
		if state.assistantAppended && state.assistant.Type == BlockAssistant {
			nonWS := visibleAssistantStreamContent(state.assistant.Content)
			if assistantStreamContentIsPlaceholder(nonWS) {
				shouldDiscard = true
			}
			// Check if content is 1-3 chars or a single short word (<=5 chars, letters only)
			isVeryShort := len(nonWS) <= 3 || (len(nonWS) <= 5 && isASCIILettersOnly(nonWS))
			if isVeryShort {
				// Check if previous visible block is a completed tool call
				blocks := m.viewport.visibleBlocks()
				for i, b := range blocks {
					if b == state.assistant {
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
			m.removeViewportBlockByID(state.assistant.ID)
			state.assistant = nil
			state.assistantAppended = false
			m.storeStreamState(agentID, state)
			m.flushPendingLocalStatusCards()
			return
		}
		state.assistant.InvalidateCache()
		if state.assistantAppended {
			m.markBlockSettled(state.assistant)
		}
		m.viewport.UpdateBlock(state.assistant.ID)
		m.syncStartupDeferredTranscriptBlock(state.assistant)
		state.assistant = nil
		state.assistantAppended = false
	}
	m.storeStreamState(agentID, state)
	m.flushPendingLocalStatusCards()
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
	m.finalizeAgentStream("")
}

// revealTrailingInterruptedTurnUserMessage keeps an explicitly interrupted
// turn anchored at its prompt when the partial reply is taller than the
// viewport. The transcript still retains the partial reply below the prompt.
func (m *Model) revealTrailingInterruptedTurnUserMessage() bool {
	if m == nil || m.agent == nil || m.viewport == nil {
		return false
	}
	msgs := m.agent.GetMessages()
	if len(msgs) < 2 {
		return false
	}
	last := msgs[len(msgs)-1]
	if last.Role != message.RoleAssistant || last.StopReason != "interrupted" {
		return false
	}

	userMsgIndex := -1
	for i := len(msgs) - 2; i >= 0; i-- {
		if msgs[i].Role == message.RoleUser && !msgs[i].IsCompactionSummary {
			userMsgIndex = i
			break
		}
	}
	if userMsgIndex < 0 {
		return false
	}

	blocks := m.viewport.visibleBlocks()
	starts := m.viewport.blockStarts()
	if len(starts) != len(blocks) {
		return false
	}
	for i, block := range blocks {
		if block == nil || block.Type != BlockUser || block.MsgIndex != userMsgIndex {
			continue
		}
		start := starts[i]
		end := start + m.viewport.blockSpanAt(blocks, i, block)
		viewportEnd := m.viewport.offset + m.viewport.height
		if end > m.viewport.offset && start < viewportEnd {
			return false
		}
		m.viewport.offset = start
		m.viewport.clampOffset()
		m.viewport.sticky = m.viewport.atBottom()
		return true
	}
	return false
}

func (m *Model) stageDraft(draft queuedDraft) tea.Cmd {
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
		PDFNames:   pdfNamesFromContentParts(draft.contentParts()),
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
		if len(draft.Parts) > 0 && draft.Parts[0].Type == message.ContentPartText {
			content = draft.Parts[0].Text
		}
		m.setTerminalTitleFromMessage(content)
	}
	m.syncVisibleMainUserBlockMsgIndexes()
	return m.imageProtocolCmd()
}

func (m *Model) sendDraft(draft queuedDraft) tea.Cmd {
	cmd := m.stageDraft(draft)
	if m.agent != nil {
		if len(draft.Parts) > 0 {
			m.agent.SendUserMessageWithParts(draft.Parts)
		} else {
			m.agent.SendUserMessage(draft.Content)
		}
	}
	return cmd
}

func (m *Model) drainQueuedDrafts() tea.Cmd {
	agentID := normalizeDraftAgentID(m.focusedAgentID)
	idx := -1
	for i, draft := range m.queuedDrafts {
		if normalizeDraftAgentID(draft.AgentID) == agentID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	draft := m.removeQueuedDraftAt(idx)
	if draft.QueuedAt.IsZero() {
		draft.QueuedAt = time.Now()
	}
	m.finalizeTurn()
	protoCmd := m.sendDraft(draft)
	m.recalcViewportSize()
	return tea.Batch(m.startAnimTick(), protoCmd)
}

func (m *Model) resetStreamingToIdle() {
	m.finalizeAssistantBlock()
	m.markAgentIdle(identity.MainAgentID)
	m.stopActiveAnimationIfIdle()
}

// touchStreamDelta records that a visible streaming delta was received for the
// given agent. streamingStale uses this timestamp to distinguish "long-running
// but active streaming" from "connection silently lost".
func (m *Model) touchStreamDelta(agentID string) {
	if agentID == "" {
		agentID = identity.MainAgentID
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
		aid = identity.MainAgentID
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
		return identity.MainAgentID
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
	backend := m.agent
	focusedAgentID := m.focusedAgentID
	if m.focusedAgentIDOrMain() != "main" {
		target := m.focusedConversationTarget(focusedAgentID)
		targeted, hasTargeted := backend.(agent.TargetedConversationController)
		return tea.Batch(m.startActiveAnimation(), func() tea.Msg {
			msgs := backend.GetMessages()
			if hasTargeted {
				msgs = targeted.GetMessagesForTarget(target)
			}
			if len(msgs) == 0 {
				return focusedContinueActionMsg{target: target}
			}
			last := msgs[len(msgs)-1]
			switch last.Role {
			case message.RoleUser, message.RoleTool:
				return focusedContinueActionMsg{action: focusedContinueFromContext, target: target}
			case message.RoleAssistant:
				if len(last.ToolCalls) > 0 {
					return focusedContinueActionMsg{action: focusedContinueFromContext, target: target}
				} else if len(last.ThinkingBlocks) > 0 && strings.TrimSpace(last.Content) == "" {
					return focusedContinueActionMsg{action: focusedContinueAfterRemovingLast, target: target}
				} else if last.StopReason == "stop" || last.StopReason == "end_turn" {
					return focusedContinueActionMsg{action: focusedContinueWithDraft, target: target}
				} else {
					return focusedContinueActionMsg{action: focusedContinueFromContext, target: target}
				}
			}
			return nil
		})
	}
	msgs := backend.GetMessages()
	if len(msgs) == 0 {
		return nil
	}
	last := msgs[len(msgs)-1]
	switch last.Role {
	case message.RoleUser, message.RoleTool:
		backend.ContinueFromContext()
	case message.RoleAssistant:
		if len(last.ToolCalls) > 0 {
			backend.ContinueFromContext()
		} else if len(last.ThinkingBlocks) > 0 && strings.TrimSpace(last.Content) == "" {
			backend.RemoveLastMessage()
			backend.ContinueFromContext()
		} else if last.StopReason == "stop" || last.StopReason == "end_turn" {
			return tea.Batch(m.startActiveAnimation(), m.sendDraft(queuedDraft{Content: "continue"}))
		} else {
			backend.ContinueFromContext()
		}
	}
	return m.startActiveAnimation()
}

func (m *Model) focusedConversationTarget(agentID string) agent.ConversationTarget {
	target := agent.ConversationTarget{AgentID: agentID}
	for _, sub := range m.agent.GetSubAgents() {
		if sub.InstanceID == agentID {
			target.TaskID = sub.TaskID
			break
		}
	}
	return target
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
		if msgs[i].Role == message.RoleUser {
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
		parts = []message.ContentPart{{Type: message.ContentPartText, Text: last.Content}}
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
