package agent

import (
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// ResolveConfirm sends the user's confirmation response back to the waiting
// ConfirmFunc goroutine via the broker's requestID→channel map. The resolve
// path acquires only the map lock (never a flow lock) to avoid deadlock.
func (a *MainAgent) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
	a.ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID, nil)
}

// ResolveConfirmWithRuleIntent sends the confirmation response with an optional
// rule intent for adding a permission overlay rule.
func (a *MainAgent) ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *ConfirmRuleIntent) {
	var copiedRuleIntent *ConfirmRuleIntent
	if ruleIntent != nil {
		intentCopy := *ruleIntent
		copiedRuleIntent = &intentCopy
	}
	a.interaction.resolveConfirm(requestID, ConfirmResponse{
		Approved:      action == "allow",
		FinalArgsJSON: finalArgsJSON,
		EditSummary:   editSummary,
		DenyReason:    denyReason,
		RuleIntent:    copiedRuleIntent,
	})
}

// ResolveQuestion sends the user's question response back to the waiting
// QuestionFunc goroutine via the broker's requestID→channel map. The resolve
// path acquires only the map lock (never a flow lock) to avoid deadlock.
func (a *MainAgent) ResolveQuestion(answers []string, cancelled bool, requestID string) {
	a.interaction.resolveQuestion(requestID, QuestionResponse{Answers: answers, Cancelled: cancelled})
}

// ClearPendingInteractions removes requestID mappings for any in-flight
// confirm/question requests. It does not close the per-request channels; any
// waiters are expected to exit via ctx cancellation or stoppingCh during
// shutdown.
func (a *MainAgent) ClearPendingInteractions() {
	a.interaction.clearPending()
}

// AgentContextUsage holds input-context stats for one agent (main or sub) for sidebar display.
type AgentContextUsage struct {
	AgentID             string
	ContextCurrent      int
	ContextLimit        int
	ContextMessageCount int
}

// GetSubAgents returns live and parked SubAgents for TUI sidebar display.
// Safe to call from any goroutine.
func (a *MainAgent) GetSubAgents() []SubAgentInfo {
	a.subs.mu.RLock()
	infos := make([]SubAgentInfo, 0, len(a.subs.subAgents)+len(a.subs.taskRecords))
	seenTasks := make(map[string]struct{}, len(a.subs.subAgents))
	for _, sub := range a.subs.subAgents {
		selectedRef := ""
		runningRef := ""
		client, modelName := sub.llmSnapshot()
		if client != nil {
			selectedRef = client.PrimaryModelRef()
			if v := client.ActiveVariant(); v != "" {
				selectedRef += "@" + v
			}
			runningRef = formatModelRefForNotification(client.RunningModelRef(), selectedRef, client.ActiveVariant())
		}
		state := sub.State()
		summary := sub.LastSummary()
		artifact := sub.LastArtifact()
		infos = append(infos, SubAgentInfo{
			InstanceID:       sub.instanceID,
			TaskID:           sub.taskID,
			AgentDefName:     sub.agentDefName,
			TaskDesc:         sub.taskDesc,
			ModelName:        modelName,
			SelectedRef:      selectedRef,
			RunningRef:       runningRef,
			State:            string(state),
			Color:            sub.color,
			LastSummary:      summary,
			UrgentInboxCount: a.subAgentUrgentInboxCountLocked(sub.instanceID),
			LastArtifact:     artifact,
		})
		seenTasks[sub.taskID] = struct{}{}
	}
	for taskID, rec := range a.subs.taskRecords {
		if rec == nil || !rec.RuntimeParked || strings.TrimSpace(rec.LatestInstanceID) == "" {
			continue
		}
		if _, ok := seenTasks[taskID]; ok {
			continue
		}
		var artifact tools.ArtifactRef
		if len(rec.LastArtifactRefs) > 0 {
			artifact = tools.NormalizeArtifactRef(rec.LastArtifactRefs[0])
		}
		selectedRef := a.restoredSubAgentModelRef(rec)
		infos = append(infos, SubAgentInfo{
			InstanceID:       rec.LatestInstanceID,
			TaskID:           taskID,
			AgentDefName:     rec.AgentDefName,
			TaskDesc:         rec.TaskDesc,
			SelectedRef:      selectedRef,
			RunningRef:       restoredRunningModelRef(rec, selectedRef),
			State:            rec.State,
			LastSummary:      rec.LastSummary,
			UrgentInboxCount: a.subAgentUrgentInboxCountLocked(rec.LatestInstanceID),
			LastArtifact:     artifact,
		})
	}
	a.subs.mu.RUnlock()
	return infos
}

func (a *MainAgent) restoredSubAgentModelRef(rec *DurableTaskRecord) string {
	if rec == nil {
		return ""
	}
	if ref := strings.TrimSpace(rec.SelectedModelRef); ref != "" {
		return ref
	}
	a.stateMu.RLock()
	cfg := a.agentConfigs[strings.TrimSpace(rec.AgentDefName)]
	a.stateMu.RUnlock()
	if ref := a.defaultRoleModelRef(cfg); ref != "" {
		return ref
	}
	return strings.TrimSpace(a.ProviderModelRef())
}

func restoredRunningModelRef(rec *DurableTaskRecord, selected string) string {
	if rec != nil {
		if ref := strings.TrimSpace(rec.RunningModelRef); ref != "" {
			return ref
		}
	}
	return strings.TrimSpace(selected)
}

func (a *MainAgent) subAgentUrgentInboxCountLocked(agentID string) int {
	a.subAgentInboxSummaryMu.RLock()
	defer a.subAgentInboxSummaryMu.RUnlock()
	return a.subAgentUrgentCounts[agentID]
}

// GetMessages returns a thread-safe snapshot of the focused agent's conversation
// history. Routes to the focused SubAgent if one is active.
func (a *MainAgent) GetMessages() []message.Message {
	return a.GetMessagesForTarget(a.focusedConversationTarget())
}

// GetMessagesForTarget returns a snapshot for a previously captured target.
func (a *MainAgent) GetMessagesForTarget(conversation ConversationTarget) []message.Message {
	target, ok := a.resolveConversationTarget(conversation)
	if !ok {
		return nil
	}
	if target.sub != nil {
		return target.sub.GetMessages()
	}
	if target.parked {
		msgs, err := loadTaskHistoryMessages(a.recovery, target.task)
		if err != nil {
			log.Warnf("GetMessages: failed to load parked subagent transcript task_id=%v error=%v", target.task.TaskID, err)
			return nil
		}
		return msgs
	}
	return a.ctxMgr.Snapshot()
}

// ContinueFromContext re-runs the LLM with the existing context without
// appending a new user message. Routes to the focused SubAgent if active.
func (a *MainAgent) ContinueFromContext() {
	a.ContinueFromContextForTarget(a.focusedConversationTarget())
}

// ContinueFromContextForTarget re-runs the LLM for a captured conversation.
func (a *MainAgent) ContinueFromContextForTarget(conversation ConversationTarget) {
	target, ok := a.resolveConversationTarget(conversation)
	if !ok {
		a.emitToTUI(ToastEvent{Message: "Conversation is no longer available; retry the action", Level: "warn", AgentID: conversation.AgentID})
		return
	}
	if sub := target.sub; sub != nil {
		state := sub.State()
		restartStoppedTurn := state != SubAgentStateRunning
		switch state {
		case SubAgentStateRunning:
		default:
			if err := a.acquireSubAgentSlot(sub); err != nil {
				a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: sub.instanceID})
				return
			}
			a.markSubAgentReactivated(sub, "Resumed from existing context")
			a.saveRecoverySnapshot()
			a.persistSubAgentMeta(sub)
			a.syncTaskRecordFromSub(sub, "")
		}
		a.mailboxDeliveryPaused.Store(false)
		sub.continueWithContextAppends(a.drainOwnedSubAgentMailboxes(sub.instanceID), restartStoppedTurn)
		return
	}
	if rec := target.task; target.parked && rec != nil {
		sub, _, err := a.rehydrateTask(rec)
		if err != nil {
			a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn", AgentID: rec.LatestInstanceID})
			return
		}
		if !a.reactivateFocusedSubAgentForManualInput(sub) {
			return
		}
		a.mailboxDeliveryPaused.Store(false)
		sub.continueWithContextAppends(a.drainOwnedSubAgentMailboxes(sub.instanceID), true)
		return
	}
	a.mailboxDeliveryPaused.Store(false)
	a.sendEvent(Event{Type: EventContinue, Payload: manualContinueEvent{}})
}

// RemoveLastMessage removes the last message from context and rewrites the
// persistence log. Routes to the focused SubAgent if active.
// Only valid when the agent is idle.
func (a *MainAgent) RemoveLastMessage() {
	a.RemoveLastMessageForTarget(a.focusedConversationTarget())
}

// RemoveLastMessageForTarget removes the last message from a captured target.
func (a *MainAgent) RemoveLastMessageForTarget(conversation ConversationTarget) {
	target, ok := a.resolveConversationTarget(conversation)
	if !ok {
		return
	}
	if target.sub != nil {
		target.sub.RemoveLastMessage()
		return
	}
	if target.parked {
		msgs, err := loadTaskHistoryMessages(a.recovery, target.task)
		if err != nil {
			log.Warnf("RemoveLastMessage: failed to load parked subagent transcript task_id=%v error=%v", target.task.TaskID, err)
			return
		}
		if len(msgs) == 0 {
			return
		}
		if err := rewriteTaskHistoryMessages(a.recovery, target.task, msgs[:len(msgs)-1]); err != nil {
			log.Warnf("RemoveLastMessage: failed to rewrite parked subagent transcript task_id=%v error=%v", target.task.TaskID, err)
		}
		return
	}
	a.turnMu.Lock()
	idle := a.turn == nil
	a.turnMu.Unlock()
	if !idle {
		return
	}
	a.ctxMgr.DropLastMessage()
	if a.recovery != nil {
		remaining := a.ctxMgr.Snapshot()
		if err := a.recovery.RewriteLog("main", remaining); err != nil {
			log.Warnf("RemoveLastMessage: failed to rewrite main log error=%v", err)
		}
	}
}

// handleContinueFromContext starts a new turn and calls LLM without appending
// any new user message.
func (a *MainAgent) handleContinueFromContext(evt Event) {
	if a.turn != nil {
		log.Debug("handleContinueFromContext: ignored, turn already active")
		return
	}
	if _, manual := evt.Payload.(manualContinueEvent); manual {
		a.stageNextSubAgentMailboxBatch()
	}
	a.applyPendingCompactionResumeOverlaysForContinue()
	if a.loopState.Enabled {
		a.loopState.State = LoopStateExecuting
		a.emitLoopStateChanged()
	}
	a.newTurn()
	a.processPendingUserMessagesBeforeLLMInTurn()
	turnID := a.turn.ID
	turnCtx := a.turn.Ctx
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
}

// FocusedAgentName returns the agent definition name of the currently focused
// SubAgent, or "" if the main agent is focused.
func (a *MainAgent) FocusedAgentName() string {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.agentDefName
	}
	if rec := a.focusedDurableTask(); rec != nil {
		return rec.AgentDefName
	}
	return ""
}

// FocusedAgentID returns the instance ID of the currently focused SubAgent,
// or "" if the main agent is focused.
func (a *MainAgent) FocusedAgentID() string {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.instanceID
	}
	if rec := a.focusedDurableTask(); rec != nil {
		return rec.LatestInstanceID
	}
	return ""
}
