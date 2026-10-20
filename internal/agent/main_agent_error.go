package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/hook"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// handleAgentError emits the error to the TUI and logs it. An IdleEvent is
// also sent so the TUI knows the agent is ready for new input.
//
// If SourceID is "main" or empty, this is the MainAgent's own error.
// If SourceID identifies a SubAgent, its active resources are released and the
// failed instance is retained so the user can explicitly continue it later.
func (a *MainAgent) handleAgentError(evt Event) {
	err, ok := evt.Payload.(error)
	if !ok {
		log.Errorf("handleAgentError: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	// Guard: SourceID "main" or empty means MainAgent's own LLM/tool error —
	// no SubAgent to clean up.
	if evt.SourceID == identity.MainAgentID || evt.SourceID == "" {
		// Turn isolation: discard errors from cancelled/stale turns.
		if a.turn != nil && evt.TurnID != 0 && evt.TurnID != a.turn.ID {
			log.Debugf("discarding stale error event_turn=%v current_turn=%v", evt.TurnID, a.currentTurnID())
			return
		}
		// Settle the request before the routing-invalidation restart:
		// applyPendingModelPoolSwitchesAtRequestBoundary is a no-op while a main
		// request is still marked in flight, and this is exactly the boundary a
		// pool switch that invalidated the routing must be applied at.
		a.mainLLMRequestInFlight.Store(false)
		if !llm.IsNativeToolError(err) && llm.IsRoutingInvalidated(err) {
			log.Infof("routing invalidated during active turn; restarting request turn_id=%v instance=%v", evt.TurnID, a.instanceID)
			a.applyPendingModelPoolSwitchesAtRequestBoundary()
			// The restart can land on a different model or backend, and a
			// finalized reasoning item only replays to the one that produced
			// it. The partial text is provider-neutral and stays.
			a.turn.drainPartialResponsesOutput()
			if a.resumeTurnAfterRoutingInvalidation(evt.TurnID) {
				return
			}
		}

		// A stream interruption that carried already-streamed assistant text is
		// resumable: the partial reply is saved to history (never discarded),
		// followed by a durable continuation message for pools that need one,
		// and the turn restarts the request so the model continues from where
		// it stopped. The restart runs the full retry rotation (key switch,
		// fallback models, cooling waits) without a round cap, so a
		// persistently failing transport behaves like any other error without
		// ever throwing away produced text; the client cools the interrupted
		// key before escalating so repeated interruptions rotate keys and
		// fallback models and wait out cooldowns rather than restarting
		// back-to-back. When nothing visible was streamed, this falls through
		// to ordinary error handling below — which then discards nothing,
		// since the partial text has already been drained and saved.
		if a.turn != nil && !llm.IsNativeToolError(err) && llm.IsPreservableStreamInterruption(err) {
			if a.resumeAfterPreservedStreamInterruption(evt.TurnID, err) {
				return
			}
		}

		log.Errorf("agent error error=%v turn_id=%v instance=%v", err, evt.TurnID, a.instanceID)
		a.discardPartialAssistantMsg()
		a.failPendingToolCalls(a.turn, err)
		a.applyPendingModelPoolSwitchesAtRequestBoundary()
		a.fireHookBackground(a.parentCtx, hook.OnAgentError, evt.TurnID, map[string]any{
			"message":         err.Error(),
			"error_kind":      classifyAgentError(err),
			"source_agent_id": a.instanceID,
		})
		a.emitToTUI(StreamRollbackEvent{Reason: err.Error()})
		a.emitToTUI(ErrorEvent{Err: err})
		if a.turn != nil {
			outcome := recovery.DeliveryWorkFailed
			if llm.IsNativeToolError(err) {
				outcome = recovery.DeliveryWorkUnknown
			}
			a.settleDeliveryWork(a.turn.DeliveryWorkID, outcome)
		}
		a.stopLoopAsBlocked(err.Error())
		a.markActiveSubAgentMailboxAck(false)
		a.setIdleAndDrainPending()
		return
	}

	// SubAgent error: clean up the failed agent (same as handleAgentDone minus
	// LLM review).
	log.Errorf("SubAgent error error=%v source=%v", err, evt.SourceID)

	var emitCalls, persistCalls, discardCalls []PendingToolCall
	sub := a.subs.subAgent(evt.SourceID)
	if sub != nil {
		emitCalls, persistCalls = sub.drainPendingToolFailureSets(err)
		emitCalls, discardCalls = splitPendingCallsByDeclaredTools(sub.ctxMgr, emitCalls)
	}
	if len(persistCalls) > 0 && sub != nil {
		persistedResults := sub.persistInterruptedToolResults(persistCalls, message.ToolStatusError, err)
		if persistedResults > 0 {
			// The SubAgent is about to enter its terminal close path. Ensure the
			// synthetic tool results queued above are durable before restore,
			// export, or cleanup can observe the failed instance.
			a.flushPersist()
			log.Infof("persisted failed sub-agent tool-call results after terminal error agent=%v count=%v", evt.SourceID, persistedResults)
		}
	}
	if len(emitCalls) > 0 {
		emitFailedToolResults(a.emitToTUI, emitCalls, err)
		a.emitActivity(evt.SourceID, ActivityIdle, "")
	}
	if len(discardCalls) > 0 {
		emitToolCallDiscards(a.emitToTUI, discardCalls, "not_in_context")
		a.emitActivity(evt.SourceID, ActivityIdle, "")
	}

	a.hookEngine.FireBackground(a.parentCtx, newHookEnvelope(
		hook.OnAgentError,
		a.sessionDir,
		evt.TurnID,
		evt.SourceID,
		"sub",
		a.effectiveToolBaseDir(),
		"",
		"",
		map[string]any{
			"message":         err.Error(),
			"error_kind":      classifyAgentError(err),
			"source_agent_id": evt.SourceID,
		},
	))

	sub2 := a.subAgentByID(evt.SourceID)
	if sub2 != nil {
		errorKind := classifyAgentError(err)
		failureSummary := fmt.Sprintf("SubAgent failed (%s): %s", errorKind, err.Error())
		a.releaseSubAgentSlot(sub2)
		a.emitActivity(evt.SourceID, ActivityIdle, "")
		mailbox := &SubAgentMailboxMessage{
			AgentID:      evt.SourceID,
			TaskID:       sub2.taskID,
			OwnerAgentID: sub2.OwnerAgentID(),
			OwnerTaskID:  sub2.OwnerTaskID(),
			InReplyTo:    firstReplyMessageID(sub2),
			Kind:         SubAgentMailboxKindRiskAlert,
			Subtype:      agentMessageSubtypeTaskFailure,
			Priority:     SubAgentMailboxPriorityInterrupt,
			Summary:      failureSummary,
			Payload: fmt.Sprintf(
				"SubAgent task terminated without completion.\n- task_id: %s\n- agent_id: %s\n- error_kind: %s\n- error: %s\n- required_action: inspect the failure and retry, reassign, or report the blocker; do not treat the task as completed.",
				sub2.taskID, evt.SourceID, errorKind, err.Error(),
			),
			RequiresAck: false,
		}
		// Terminal-commit ordering (mirror of the completion path in
		// handleAgentDone): persist and apply the risk_alert mailbox before the
		// close handler below commits the task Failed, so a crash after the
		// terminal commit can no longer lose the failure notification; the
		// queued mailbox event then only delivers the already-durable message.
		// Persistence stays best-effort: a failure is retried through the
		// queued event's own persist path and must never block the terminal
		// commit.
		if err := a.prepareSubAgentMailboxMessage(mailbox); err != nil {
			log.Warnf("risk_alert mailbox durability degraded task_id=%v agent_id=%v error=%v (will retry through the mailbox queue)", sub2.taskID, evt.SourceID, err)
		} else if messageID := strings.TrimSpace(mailbox.MessageID); messageID != "" {
			// Already durably recorded and applied here; the mailbox event must
			// only deliver it, not write or apply it a second time.
			a.markSubAgentMailboxSeen(messageID)
		}
		a.queueLoopEvent(Event{Type: EventSubAgentMailbox, SourceID: evt.SourceID, Payload: mailbox})
		a.emitToTUI(AgentNotifyEvent{
			AgentID:       evt.SourceID,
			TaskID:        sub2.taskID,
			AgentType:     sub2.agentDefName,
			ParentAgentID: controlPlaneAgentID(sub2.OwnerAgentID()),
			ParentTaskID:  sub2.OwnerTaskID(),
			TargetAgentID: controlPlaneAgentID(sub2.OwnerAgentID()),
			TargetTaskID:  sub2.OwnerTaskID(),
			Kind:          string(SubAgentMailboxKindRiskAlert),
			Message:       failureSummary,
		})
		a.handleSubAgentCloseRequestedEvent(Event{
			Type:     EventSubAgentCloseRequested,
			SourceID: evt.SourceID,
			Payload: &SubAgentCloseRequestedPayload{
				Reason:       err.Error(),
				ClosedReason: "subagent failed",
				FinalState:   SubAgentStateFailed,
			},
		})
	}

	a.emitToTUI(ErrorEvent{Err: fmt.Errorf("SubAgent %s error: %w", evt.SourceID, err)})
}
