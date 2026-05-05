package agent

import (
	"context"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/message"
)

// CancelCurrentTurn cancels the agent's active turn (if any), aborting any
// in-flight LLM call or tool execution. It is safe to call from any goroutine
// (typically the TUI's Ctrl+C handler). Returns true if a turn was active and
// cancelled, false if the agent was already idle.
//
// If the turn had pending tool calls, synthetic terminal tool-result messages
// are appended when the cancellation event is handled so the conversation and
// persisted session keep a matching output for each tool call.
func (a *MainAgent) CancelCurrentTurn() bool {
	a.turnMu.Lock()
	t := a.turn
	a.turnMu.Unlock()

	cancelled := false
	if t != nil {
		pending := t.PendingToolCalls.Load()
		if t.activeToolBatchCancel != nil {
			t.activeToolBatchCancel()
			t.activeToolBatchCancel = nil
		}
		t.Cancel()
		cancelledExec := t.cancelPendingToolCalls()
		cancelledStream := t.drainStreamingToolCalls()
		merged := mergePendingToolCalls(cancelledExec, cancelledStream)
		a.clearToolTraceForCalls(merged)
		a.sendEvent(Event{
			Type:   EventTurnCancelled,
			TurnID: t.ID,
			Payload: &TurnCancelledPayload{
				TurnID:                               t.ID,
				Calls:                                merged,
				MarkToolCallsFailed:                  true,
				KeepPendingUserMessagesQueued:        true,
				CommitPendingUserMessagesWithoutTurn: true,
			},
		})
		log.Infof("current turn interrupted by user turn_id=%v instance=%v pending_tools=%v failed_tools=%v", t.ID, a.instanceID, pending, len(merged))
		cancelled = true
	}
	if a.interruptSubAgentTurnsForUserCancel() {
		cancelled = true
	}
	return cancelled
}

func cloneContentParts(parts []message.ContentPart) []message.ContentPart {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]message.ContentPart, len(parts))
	copy(cloned, parts)
	for i := range cloned {
		if cloned[i].Data != nil {
			cloned[i].Data = append([]byte(nil), cloned[i].Data...)
		}
	}
	return cloned
}

func pendingUserMessageFromDraft(draftID string, parts []message.ContentPart) pendingUserMessage {
	cloned := cloneContentParts(parts)
	var b strings.Builder
	for _, part := range cloned {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return pendingUserMessage{
		DraftID:  draftID,
		Content:  b.String(),
		Parts:    cloned,
		FromUser: true,
	}
}

func pendingDraftParts(p pendingUserMessage) []message.ContentPart {
	if len(p.Parts) > 0 {
		return cloneContentParts(p.Parts)
	}
	if p.Content == "" {
		return nil
	}
	return []message.ContentPart{{Type: "text", Text: p.Content}}
}

func pendingUserMessageText(p pendingUserMessage) string {
	if strings.TrimSpace(p.Content) != "" {
		return p.Content
	}
	var sb strings.Builder
	for _, part := range p.Parts {
		if part.Type == "text" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func (a *MainAgent) recordCommittedUserMessage(userMsg message.Message) {
	a.ctxMgr.Append(userMsg)
	a.recordEvidenceFromMessage(userMsg)
	if a.usageLedger != nil {
		if err := a.usageLedger.SetFirstUserMessage(message.UserPromptPlainText(userMsg)); err != nil {
			log.Warnf("failed to update usage summary first user message error=%v", err)
		}
		a.updateSessionSummary(func(summary *SessionSummary) {
			if summary == nil {
				return
			}
			if summary.FirstUserMessage == "" {
				summary.FirstUserMessage = message.UserPromptPlainText(userMsg)
			}
		})
	}
	if a.recovery != nil {
		a.persistAsync("main", userMsg)
	}
}

func (a *MainAgent) pendingUserMessageToConversationMessage(p pendingUserMessage) (message.Message, bool) {
	content := pendingUserMessageText(p)
	if a.handleLocalOnlySlashCommands(content, p.Parts) {
		return message.Message{}, false
	}
	outC, outP := a.expandSlashCommandForModel(content, p.Parts)
	outC, outP = a.filterUnsupportedParts(outC, outP)
	return message.Message{Role: "user", Content: outC, Parts: outP}, true
}

func (a *MainAgent) injectGitStatusIntoFirstUserMessage(messages []message.Message) bool {
	if len(messages) == 0 || a.gitStatusInjected.Load() {
		return false
	}
	_, gitStatus, _, _ := a.promptMetaSnapshot()
	if strings.TrimSpace(gitStatus) == "" {
		return false
	}
	for i := range messages {
		if messages[i].Role != "user" {
			continue
		}
		if !a.gitStatusInjected.CompareAndSwap(false, true) {
			return false
		}
		if len(messages[i].Parts) > 0 {
			parts := make([]message.ContentPart, 0, len(messages[i].Parts)+1)
			parts = append(parts, message.ContentPart{Type: "text", Text: gitStatus + "\n\n"})
			parts = append(parts, cloneContentParts(messages[i].Parts)...)
			messages[i].Parts = parts
			return true
		}
		messages[i].Content = gitStatus + "\n\n" + messages[i].Content
		return true
	}
	return false
}

func messagePartsForTUI(msg message.Message) []message.ContentPart {
	if len(msg.Parts) > 0 {
		return cloneContentParts(msg.Parts)
	}
	if msg.Content == "" {
		return nil
	}
	return []message.ContentPart{{Type: "text", Text: msg.Content}}
}

func (a *MainAgent) emitPendingDraftConsumed(draftID string, msg message.Message) {
	if strings.TrimSpace(draftID) == "" {
		return
	}
	a.emitToTUI(PendingDraftConsumedEvent{
		DraftID: draftID,
		Parts:   messagePartsForTUI(msg),
	})
}

func (a *MainAgent) commitPendingUserMessagesWithoutTurn() {
	if len(a.pendingUserMessages) == 0 {
		return
	}
	pending := a.pendingUserMessages
	a.pendingUserMessages = nil

	deferred := make([]pendingUserMessage, 0, len(pending))
	committed := 0
	for _, p := range pending {
		if !p.FromUser {
			deferred = append(deferred, p)
			continue
		}
		content := strings.TrimSpace(pendingUserMessageText(p))
		if strings.HasPrefix(content, "/") {
			deferred = append(deferred, p)
			continue
		}
		userMsg, ok := a.pendingUserMessageToConversationMessage(p)
		if !ok {
			continue
		}
		a.recordCommittedUserMessage(userMsg)
		a.emitPendingDraftConsumed(p.DraftID, userMsg)
		committed++
	}
	if len(deferred) > 0 {
		a.pendingUserMessages = deferred
	}
	if committed > 0 {
		a.syncBugTriagePromptFromSnapshot()
		log.Debugf("committed pending user messages without starting llm turn count=%v deferred=%v", committed, len(deferred))
	}
}

func upsertPendingDraft(queue []pendingUserMessage, pending pendingUserMessage) []pendingUserMessage {
	if pending.DraftID != "" {
		for i := range queue {
			if queue[i].DraftID == pending.DraftID {
				queue[i] = pending
				return queue
			}
		}
	}
	return append(queue, pending)
}

func enqueuePendingUserMessage(queue []pendingUserMessage, pending pendingUserMessage) []pendingUserMessage {
	if pending.DraftID != "" {
		return upsertPendingDraft(queue, pending)
	}
	if pending.CoalesceKey != "" && len(pending.Parts) == 0 && len(queue) > 0 {
		last := &queue[len(queue)-1]
		if last.CoalesceKey == pending.CoalesceKey && len(last.Parts) == 0 {
			switch {
			case strings.TrimSpace(last.Content) == "":
				last.Content = pending.Content
			case strings.TrimSpace(pending.Content) != "":
				last.Content += "\n\n" + pending.Content
			}
			return queue
		}
	}
	return append(queue, pending)
}

func removePendingDraft(queue []pendingUserMessage, draftID string) ([]pendingUserMessage, bool) {
	for i := range queue {
		if queue[i].DraftID != draftID {
			continue
		}
		return append(queue[:i], queue[i+1:]...), true
	}
	return queue, false
}

// interruptCurrentTurnForReplacement terminates any still-active main-agent turn
// because another runtime control path is about to replace it with a fresh turn
// (e.g. background completion, wake-main, session switch). Unlike explicit user
// cancellation, this path persists interruption-style terminal tool results for
// already-declared pending calls so transcript/recovery remain valid for strict
// tool-call APIs.
func (a *MainAgent) interruptCurrentTurnForReplacement() {
	if a.turn == nil {
		return
	}
	turn := a.turn
	pending := turn.PendingToolCalls.Load()
	a.savePartialAssistantMsgForTurn(turn)
	cancelledExec := turn.cancelPendingToolCalls()
	cancelledStream := turn.drainStreamingToolCalls()
	merged := mergePendingToolCalls(cancelledExec, cancelledStream)
	if len(merged) > 0 {
		persistedResults := a.persistInterruptedToolResults(merged, ToolResultStatusError, context.Canceled)
		if persistedResults > 0 {
			log.Infof("persisted interrupted tool-call results before starting replacement turn turn_id=%v count=%v pending_tools=%v", turn.ID, persistedResults, pending)
		}
		emitFailedToolResults(a.emitToTUI, merged, context.Canceled)
	}
	turn.PendingToolCalls.Store(0)
	turn.TotalToolCalls.Store(0)
	turn.toolExecutionBatches = nil
	turn.nextToolBatch = 0
	if turn.activeToolBatchCancel != nil {
		turn.activeToolBatchCancel()
		turn.activeToolBatchCancel = nil
	}
	turn.Cancel()
}

// newTurn cancels any in-flight work and creates a fresh Turn.
func (a *MainAgent) newTurn() {
	a.turnMu.Lock()
	if a.turn != nil {
		a.interruptCurrentTurnForReplacement()
	}
	a.pendingHandoff = nil // clear stale deferred PlanComplete from previous turn
	a.nextTurnID++
	a.turnEpoch++
	ctx, cancel := context.WithCancel(a.parentCtx)
	a.turn = &Turn{
		ID:                    a.nextTurnID,
		Epoch:                 a.turnEpoch,
		Ctx:                   ctx,
		Cancel:                cancel,
		PendingToolMeta:       make(map[string]PendingToolCall),
		toolExecutionBatches:  nil,
		nextToolBatch:         0,
		activeToolBatchCancel: nil,
	}
	a.emitToTUI(RequestCycleStartedEvent{AgentID: a.instanceID, TurnID: a.turn.ID})
	a.turnMu.Unlock()
	log.Debugf("new turn created turn_id=%v", a.turn.ID)
}

func (a *MainAgent) currentTurn() *Turn {
	if a == nil {
		return nil
	}
	a.turnMu.Lock()
	t := a.turn
	a.turnMu.Unlock()
	return t
}

func (a *MainAgent) currentTurnID() uint64 {
	turn := a.currentTurn()
	if turn == nil {
		return 0
	}
	return turn.ID
}

// setIdleAndDrainPending marks the agent idle (turn = nil), emits IdleEvent, then
// drains any queued user messages and processes them in one batch (only when the
// agent was busy and messages were queued). Call this wherever IdleEvent was previously
// emitted so that queued input is injected after the model has finished.
func (a *MainAgent) setIdleAndDrainPending() {
	turnID := uint64(0)
	a.turnMu.Lock()
	if a.turn != nil {
		turnID = a.turn.ID
	}
	a.turn = nil
	a.turnMu.Unlock()
	a.setBugTriagePromptActive(false)
	pausePendingDrain := a.pausePendingUserDrainOnce
	a.pausePendingUserDrainOnce = false
	skipMailboxDrain := false
	if len(a.activeSubAgentMailboxes) > 0 || a.activeSubAgentMailbox != nil {
		batch := a.activeSubAgentMailboxes
		if len(batch) == 0 && a.activeSubAgentMailbox != nil {
			batch = []*SubAgentMailboxMessage{a.activeSubAgentMailbox}
		}
		if a.activeSubAgentMailboxAck {
			replySummary := latestAssistantReplySummary(a.ctxMgr.Snapshot())
			for _, msg := range batch {
				if msg == nil {
					continue
				}
				_, _, _ = a.markSubAgentMailboxConsumedWithReply(
					msg.AgentID,
					msg.MessageID,
					turnID,
					replySummary,
					"main_turn",
				)
			}
		} else {
			for _, msg := range batch {
				if msg == nil {
					continue
				}
				a.markSubAgentMailboxRetryable(msg.MessageID, turnID)
			}
			a.requeueActiveSubAgentMailbox()
			skipMailboxDrain = true
		}
		a.activeSubAgentMailboxes = nil
		a.activeSubAgentMailbox = nil
		a.activeSubAgentMailboxAck = false
		a.pendingSubAgentMailboxes = nil
		a.refreshSubAgentInboxSummary()
	}
	if !pausePendingDrain {
		a.maybeRunAutoCompaction()
	}
	// IdleEvent triggers finalizeAssistantBlock in TUI — dropping it leaves the
	// UI stuck in streaming state. Use blocking send (with shutdown guard) so it
	// is never silently discarded.

	// Continuation barrier: apply any ready compaction draft before going idle.
	// If the resume path already owns the idle barrier (for example, it emitted
	// IdleEvent/drained pending input itself or started a new turn), do not do it
	// again here.
	handledIdleBarrier := false
	if a.compactionState.readyDraft != nil {
		_, handledIdleBarrier = a.applyReadyDraft()
		// Always reset activity so the TUI spinner never stays on Compacting,
		// regardless of apply success. Previously only the success branch did
		// this and a failed apply would leave activity stuck at Compacting.
		a.emitActivity("main", ActivityIdle, "")
		// Successful auto-continue resumes by starting a new turn before the
		// outer idle path runs; failed auto-continue and manual idle continuations
		// may also emit IdleEvent/drain inside resumePendingMainLLMAfterCompaction.
		// In all of those cases the helper reports handledIdleBarrier=true.
	}

	if !handledIdleBarrier {
		a.emitInteractiveToTUI(a.parentCtx, IdleEvent{})
	}
	a.fireHookBackground(a.parentCtx, hook.OnIdle, turnID, map[string]any{})
	if !pausePendingDrain && !handledIdleBarrier {
		a.drainPendingUserMessages()
	}
	if a.turn == nil && !skipMailboxDrain {
		a.drainSubAgentInbox()
	}
}

func latestAssistantReplySummary(msgs []message.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "assistant" {
			continue
		}
		if text := strings.TrimSpace(msgs[i].Content); text != "" {
			return text
		}
	}
	return ""
}

// drainPendingUserMessages takes all queued user messages, clears the queue,
// runs slash-command handlers for any that are commands, and sends the rest
// to the model in one batch (newTurn + append all + single LLM call).
func (a *MainAgent) drainPendingUserMessages() {
	if len(a.pendingUserMessages) == 0 {
		return
	}
	pending := a.pendingUserMessages
	a.pendingUserMessages = nil

	type consumedPendingDraft struct {
		draftID string
		msg     message.Message
	}
	var batch []message.Message
	var consumed []consumedPendingDraft
	for _, p := range pending {
		content := pendingUserMessageText(p)
		if a.handleLocalOnlySlashCommands(content, p.Parts) {
			continue
		}
		if a.tryHandleSlashCommand(content) {
			continue
		}
		m, ok := a.pendingUserMessageToConversationMessage(p)
		if !ok {
			continue
		}
		batch = append(batch, m)
		consumed = append(consumed, consumedPendingDraft{draftID: p.DraftID, msg: m})
	}
	if len(batch) == 0 {
		return
	}

	log.Debugf("draining pending user messages count=%v", len(batch))
	a.newTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	for _, item := range consumed {
		a.recordCommittedUserMessage(item.msg)
		a.emitPendingDraftConsumed(item.draftID, item.msg)
	}
	a.syncBugTriagePromptFromSnapshot()
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}
