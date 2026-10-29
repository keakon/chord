package agent

import (
	"context"
	"fmt"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// settleCancelledConfirms reports the terminal resolution for the confirms the
// aborted turn still had parked. A confirm answered just before the
// cancellation keeps its approved/denied outcome; an unanswered one is
// reported as cancelled or, when its context deadline already expired,
// timeout. Only confirms whose waiter context is already cancelled and whose
// request was delivered are settled: this turn's own confirms qualify because
// the turn context was cancelled first, while confirms parked by subagents or
// external workers — whose contexts do not derive from the aborted turn — stay
// pending and keep waiting for a real answer.
//
// The event loop calls this while closing the turn out, before it emits idle.
// Settling from the synchronous cancel caller would make a TUI key handler
// wait for output-channel capacity that only a later Update can drain, locking
// the interface against itself. The waiter wakes through the turn's cancelled
// context and would otherwise emit the same resolution from its own goroutine,
// racing the idle close-out. The broker serializes the exactly-once claim with
// the emission, so the loser reports nothing and the resolution is always
// queued ahead of the idle that follows the turn.
func (a *MainAgent) settleCancelledConfirms() {
	a.interaction.emitCancelledConfirmResolutions(func(requestID string, identity confirmIdentity, outcome string) {
		a.emitToTUI(ConfirmResolvedEvent{
			RequestID: requestID,
			ToolName:  identity.ToolName,
			AgentID:   identity.AgentID,
			Outcome:   outcome,
		})
	})
}

// handleTurnCancelled closes any still-pending tool cards in the UI and marks
// the agent idle. It intentionally does not append tool messages to ctxMgr: a
// cancelled tool has no conversation-visible result and should not be sent back
// to the model on the next turn.
func (a *MainAgent) handleTurnCancelled(evt Event) {
	if a.turn != nil && a.turn.ID == evt.TurnID && a.turn.deliveryPending.Load() {
		a.turn.deliveryCancel = &evt
		return
	}
	// Turn isolation: ignore duplicate/stale cancel events so an old cancellation
	// cannot force a newer turn back to idle.
	if a.turn == nil || evt.TurnID == 0 || evt.TurnID != a.turn.ID {
		log.Debugf("discarding stale turn cancellation event_turn=%v current_turn=%v", evt.TurnID, a.currentTurnID())
		return
	}

	payload, ok := evt.Payload.(*TurnCancelledPayload)
	if !ok {
		log.Errorf("handleTurnCancelled: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if payload == nil {
		return
	}

	a.directoryInstructions.Load().clearTurnDiscovery()

	if payload.PauseMailboxDelivery {
		// Publish the pause on the loop before closeout requeues the batch:
		// the next idle drain must not race the off-loop cancel caller.
		a.mailboxDeliveryPaused.Store(true)
	}
	a.settleDeliveryWork(a.turn.DeliveryWorkID, recovery.DeliveryWorkCancelled)
	a.pauseQuestionWork()
	a.mainLLMRequestInFlight.Store(false)
	// Cancelling the turn ends it: nothing resumes behind the pending
	// compaction, so a round suspended oversize on a narrower fallback must
	// release that committed identity and its budgets back to the cursor the
	// next request starts from. callLLMForRequest deliberately keeps the
	// committed target while the suspension can resume, so this is the only
	// cancel path that has to realign it; every other cancel already returns
	// through that request-side realign.
	if a.compactionState.oversizeSuspended {
		if client, _ := a.mainLLMAndRef(); client != nil {
			a.syncRunningModelRefToCursorHead(client)
		}
	}
	a.savePartialAssistantMsg()
	if payload.KeepPendingUserMessagesQueued {
		a.suspendPendingUserDrain()
	}

	// A model-driven checkpoint armed by this turn must not outlive it: the
	// user just aborted exactly the work the checkpoint was meant to continue.
	a.cancelCompactionForTurnCancellation(evt.TurnID)
	// The armed manual /compact intent dies with the turn as well: the user
	// aborted the work, so neither its notice nor its restart survives. The
	// manual worker itself is NOT discarded — it outlives the turn and applies
	// at ready, mirroring usage-driven workers.
	a.clearManualCompactionIntentAndWithdrawNotice()

	// Extract completed speculative tool results before marking as failed
	var completedResults map[string]*ToolResultPayload
	if a.turn != nil && a.turn.streamingToolExec != nil {
		completedResults = a.turn.streamingToolExec.DrainCompletedResults()
	}

	// Separate tools into completed vs truly cancelled
	var reallyCancelled []PendingToolCall
	completedCount := 0
	for _, call := range a.turn.filterCompletedToolCalls(payload.Calls) {
		if result, ok := completedResults[call.CallID]; ok {
			if a.handleCompletedInterruptedToolResult(call, result, "not_in_context") {
				completedCount++
			}
		} else {
			// Tool was truly cancelled
			reallyCancelled = append(reallyCancelled, call)
		}
	}

	status := message.ToolResultStatus(message.ToolStatusCancelled)
	if payload.MarkToolCallsFailed {
		status = message.ToolStatusError
	}

	if len(reallyCancelled) > 0 {
		persistedResults := finalizeInterruptedToolCalls(a.ctxMgr, a.emitToTUI, a.persistInterruptedToolResults, reallyCancelled, status, context.Canceled)
		if persistedResults > 0 {
			log.Infof("persisted interrupted tool-call results after cancellation turn_id=%v interrupted=%v completed=%v", evt.TurnID, persistedResults, completedCount)
		}
	} else if completedCount > 0 {
		log.Infof("preserved completed tool results after cancellation turn_id=%v completed=%v", evt.TurnID, completedCount)
	}

	if payload.CommitPendingUserMessagesWithoutTurn {
		a.commitPendingUserMessagesWithoutTurn()
	}
	a.applyPendingModelPoolSwitchesAtRequestBoundary()
	a.settleCancelledConfirms()
	a.emitActivity(identity.MainAgentID, ActivityIdle, "")
	a.markActiveSubAgentMailboxAck(false)
	a.setIdleAndDrainPending()
}

// handleTurnCancelRequested applies a cancel request that was accepted while no
// turn existed. A message accepted at or below the request's watermark closes
// as cancelled instead of running, whether its event is still queued (the
// watermark is recorded and applied when the turn starts) or already started a
// turn (that turn is cancelled here, matched by the order of the message that
// started it). Messages accepted later outrank the watermark and are untouched.
func (a *MainAgent) handleTurnCancelRequested(evt Event) {
	payload, ok := evt.Payload.(*turnCancelRequestPayload)
	if !ok || payload == nil {
		log.Errorf("handleTurnCancelRequested: invalid payload type payload_type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	a.raiseCancelUpTo(payload.AcceptedUpTo)
	if a.turn == nil {
		a.pauseQuestionWork()
		log.Debugf("cancel request recorded without an active turn accepted_up_to=%v", payload.AcceptedUpTo)
		return
	}
	// A covered message already reached the loop and started its turn, so this
	// request cancels the turn in front of it. The turn must prove it is
	// covered: a cancel request samples the watermark before it is queued, so a
	// message accepted in between can have started the turn in front of this
	// event, and a turn no raw main-agent message started is never covered.
	if !a.cancelCoversAcceptedOrder(a.turn.originAcceptedOrder) {
		log.Debugf("cancel request outranked by active turn accepted_up_to=%v origin=%v turn_id=%v", payload.AcceptedUpTo, a.turn.originAcceptedOrder, a.turn.ID)
		return
	}
	log.Infof("cancel request applied to active turn accepted_up_to=%v turn_id=%v", payload.AcceptedUpTo, a.turn.ID)
	a.handleTurnCancelled(a.abortTurn(a.turn, true))
}

func (a *MainAgent) resumeTurnAfterRoutingInvalidation(turnID uint64) bool {
	if a.turn == nil || turnID == 0 || a.turn.ID != turnID {
		return false
	}
	// The routing-invalidation resume is a dispatch boundary too: stage any
	// mailbox that arrived while the turn was paused so its next request carries
	// it alongside the queued user input.
	a.mergePendingInputsForTurnContinuation()
	turnCtx := a.turn.Ctx
	a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
	return true
}

// streamContinueMessageText is the durable user-role continuation message
// appended after a preserved stream interruption for pools whose models cannot
// resume from a trailing assistant turn. It is real transcript content: the
// same text is sent to the model in this and every later request and rendered
// as the card the user sees, so what the UI shows always equals what the model
// was told. The preserved partial reply sits directly above it in history, so
// it names what to continue instead of restating the task.
const streamContinueMessageText = "Your previous reply was interrupted before it completed. The text already written is preserved above; continue directly from the interruption point without apologizing or restating what was written."

// resumeAfterPreservedStreamInterruption saves the turn's streamed partial
// text as an interrupted assistant message and restarts the main LLM request so
// the model continues from where it stopped. The restart runs the full retry
// rotation (key switch, fallback models, cooling waits): the client cooled the
// interrupted key before escalating, so a transport that keeps truncating
// mid-stream rotates across keys and fallback models and waits out cooldowns
// instead of restarting back-to-back. Continuation is not capped: the restart
// repeats until the reply completes, an interruption carries no new visible
// text, the turn is cancelled or goes stale, or a non-resumable error ends the
// turn normally. It reports false when the turn is stale or no body text was
// streamed, letting the caller fall through to ordinary error handling.
// Event-loop-goroutine only.
func (a *MainAgent) resumeAfterPreservedStreamInterruption(turnID uint64, cause error) bool {
	if a.turn == nil || turnID == 0 || a.turn.ID != turnID {
		return false
	}
	if !a.savePartialAssistantMsgForTurn(a.turn) {
		// Nothing visible was streamed before the interruption, so there is no
		// partial reply to resume; fall through to ordinary error handling.
		return false
	}
	log.Infof("resuming turn after preserved stream interruption turn_id=%v error=%v", turnID, cause)
	a.prepareStreamContinuation()
	a.resumeTurnAfterRoutingInvalidation(turnID)
	return true
}

// prepareStreamContinuation readies the restart of a preserved interruption.
// The saved partial reply is already the last durable message, so a model pool
// that can resume from a trailing assistant turn needs nothing else: the next
// request simply ends with the interrupted reply and the model picks it up. A
// pool that needs a user turn instead gets a durable KindStreamContinue
// message appended right after the partial, so the transcript and every later
// request carry exactly what the model was sent. The StreamContinueEvent
// mirrors that appended message (empty when nothing was appended) so the TUI
// settles the interrupted card and, when a message exists, renders it.
func (a *MainAgent) prepareStreamContinuation() {
	if a.llmClient != nil && a.llmClient.AllPoolTargetsSupportAssistantPrefillContinuation() {
		log.Debugf("resuming preserved stream via trailing assistant turn turn_id=%v", a.turn.ID)
		a.emitToTUI(StreamContinueEvent{Text: ""})
		return
	}
	msg := message.Message{Role: message.RoleUser, Content: streamContinueMessageText, Kind: message.KindStreamContinue}
	a.ctxMgr.Append(msg)
	if a.recoveryManager() != nil {
		a.persistAsync(identity.MainAgentID, msg)
	}
	log.Debugf("persisted stream continuation message turn_id=%v", a.turn.ID)
	a.emitToTUI(StreamContinueEvent{Text: streamContinueMessageText})
}
