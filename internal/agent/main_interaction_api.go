package agent

import (
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// ResolveConfirm sends the user's confirmation response back to the waiting
// ConfirmFunc goroutine via the requestID→channel map. Only acquires
// confirmMapMu (never confirmFlowMu) to avoid deadlock.
func (a *MainAgent) ResolveConfirm(action, finalArgsJSON, editSummary, denyReason, requestID string) {
	a.ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID, nil)
}

// ResolveConfirmWithRuleIntent sends the confirmation response with an optional
// rule intent for adding a permission overlay rule.
func (a *MainAgent) ResolveConfirmWithRuleIntent(action, finalArgsJSON, editSummary, denyReason, requestID string, ruleIntent *ConfirmRuleIntent) {
	a.confirmMapMu.Lock()
	ch, ok := a.confirmCh[requestID]
	a.confirmMapMu.Unlock()
	if ok {
		var copiedRuleIntent *ConfirmRuleIntent
		if ruleIntent != nil {
			intentCopy := *ruleIntent
			copiedRuleIntent = &intentCopy
		}
		select {
		case ch <- ConfirmResponse{
			Approved:      action == "allow",
			FinalArgsJSON: finalArgsJSON,
			EditSummary:   editSummary,
			DenyReason:    denyReason,
			RuleIntent:    copiedRuleIntent,
		}:
		default:
		}
	}
}

// ResolveQuestion sends the user's question response back to the waiting
// QuestionFunc goroutine via the requestID→channel map. Only acquires
// questionMapMu (never questionFlowMu) to avoid deadlock.
func (a *MainAgent) ResolveQuestion(answers []string, cancelled bool, requestID string) {
	a.questionMapMu.Lock()
	ch, ok := a.questionCh[requestID]
	a.questionMapMu.Unlock()
	if ok {
		select {
		case ch <- QuestionResponse{Answers: answers, Cancelled: cancelled}:
		default:
		}
	}
}

// ClearPendingInteractions removes requestID mappings for any in-flight
// confirm/question requests. It does not close the per-request channels; any
// waiters are expected to exit via ctx cancellation or stoppingCh during
// shutdown.
func (a *MainAgent) ClearPendingInteractions() {
	a.confirmMapMu.Lock()
	clear(a.confirmCh)
	a.confirmMapMu.Unlock()

	a.questionMapMu.Lock()
	clear(a.questionCh)
	a.questionMapMu.Unlock()
}

// AgentContextUsage holds context stats for one agent (main or sub) for sidebar display.
type AgentContextUsage struct {
	AgentID             string
	ContextCurrent      int
	ContextLimit        int
	ContextMessageCount int
}

// GetSubAgents returns information about all active SubAgents for TUI sidebar
// display. Safe to call from any goroutine.
func (a *MainAgent) GetSubAgents() []SubAgentInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()

	infos := make([]SubAgentInfo, 0, len(a.subAgents))
	for _, sub := range a.subAgents {
		selectedRef := ""
		runningRef := ""
		if sub.llmClient != nil {
			selectedRef = sub.llmClient.PrimaryModelRef()
			if v := sub.llmClient.ActiveVariant(); v != "" {
				selectedRef += "@" + v
			}
			runningRef = formatModelRefForNotification(sub.llmClient.RunningModelRef(), selectedRef, sub.llmClient.ActiveVariant())
		}
		state := sub.State()
		summary := sub.LastSummary()
		artifact := sub.LastArtifact()
		infos = append(infos, SubAgentInfo{
			InstanceID:       sub.instanceID,
			TaskID:           sub.taskID,
			AgentDefName:     sub.agentDefName,
			TaskDesc:         sub.taskDesc,
			ModelName:        sub.modelName,
			SelectedRef:      selectedRef,
			RunningRef:       runningRef,
			State:            string(state),
			Color:            sub.color,
			LastSummary:      summary,
			UrgentInboxCount: a.subAgentUrgentInboxCountLocked(sub.instanceID),
			LastArtifact:     artifact,
		})
	}
	return infos
}

func (a *MainAgent) subAgentUrgentInboxCountLocked(agentID string) int {
	a.subAgentInboxSummaryMu.RLock()
	defer a.subAgentInboxSummaryMu.RUnlock()
	return a.subAgentUrgentCounts[agentID]
}

// GetMessages returns a thread-safe snapshot of the focused agent's conversation
// history. Routes to the focused SubAgent if one is active.
func (a *MainAgent) GetMessages() []message.Message {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.GetMessages()
	}
	return a.ctxMgr.Snapshot()
}

// ContinueFromContext re-runs the LLM with the existing context without
// appending a new user message. Routes to the focused SubAgent if active.
func (a *MainAgent) ContinueFromContext() {
	if sub := a.validFocusedSubAgent(); sub != nil {
		if sub.State() != SubAgentStateRunning {
			a.emitToTUI(ToastEvent{Message: fmt.Sprintf("SubAgent %s is %s; continue is disabled", sub.instanceID, sub.State()), Level: "warn", AgentID: sub.instanceID})
			return
		}
		sub.ContinueFromContext()
		return
	}
	a.sendEvent(Event{Type: EventContinue})
}

// RemoveLastMessage removes the last message from context and rewrites the
// persistence log. Routes to the focused SubAgent if active.
// Only valid when the agent is idle.
func (a *MainAgent) RemoveLastMessage() {
	if sub := a.validFocusedSubAgent(); sub != nil {
		sub.RemoveLastMessage()
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
func (a *MainAgent) handleContinueFromContext(_ Event) {
	if a.turn != nil {
		log.Debug("handleContinueFromContext: ignored, turn already active")
		return
	}
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

// FocusedAgentID returns the instance ID of the currently focused SubAgent,
// or "" if the main agent is focused.
func (a *MainAgent) FocusedAgentID() string {
	if sub := a.validFocusedSubAgent(); sub != nil {
		return sub.instanceID
	}
	return ""
}
