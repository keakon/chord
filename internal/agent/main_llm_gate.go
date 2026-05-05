package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

type compactionContinuationKind string

type continuationPlan struct {
	kind             compactionContinuationKind
	turnID           uint64
	turnEpoch        uint64
	agentErrSourceID string
}

type compactionTarget struct {
	turnID       uint64
	turnEpoch    uint64
	sessionEpoch uint64
}

type compactionState struct {
	running           bool
	planID            uint64
	target            compactionTarget
	trigger           compactionTrigger
	discard           bool
	continuation      continuationPlan
	oversizeSuspended bool // main LLM call hit oversize while compaction running

	// Async compaction fields
	headSplit  int                // Snapshot boundary: len(messages) at compaction start
	cancel     context.CancelFunc // Cancel function for the compaction goroutine
	readyDraft *compactionDraft   // Draft waiting for continuation barrier to apply
}

const (
	compactionResumeMainLLM        compactionContinuationKind = "main_llm"
	compactionResumeLengthRecovery compactionContinuationKind = "length_recovery"
	compactionResumeIdle           compactionContinuationKind = "idle"
	// compactionResumeAutoContinue is used by threshold-based / usage-driven
	// auto compaction: once the durable summary is applied, the agent should
	// proactively start a new turn with the compacted context so the model
	// can continue progressing the task without user prompting. Manual
	// /compact still uses compactionResumeIdle (no auto continue).
	compactionResumeAutoContinue compactionContinuationKind = "auto_continue"
)

// pendingMainLLMCall remembers which continuation should resume after async compaction.
type pendingMainLLMCall struct {
	turnID            uint64
	turnEpoch         uint64
	agentErrSourceID  string
	planID            uint64
	sessionEpoch      uint64
	continuation      compactionContinuationKind
	oversizeSuspended bool
}

func (s *compactionState) isRunning() bool {
	return s != nil && s.running
}

func (s *compactionState) pendingCall() *pendingMainLLMCall {
	if s == nil || !s.running {
		return nil
	}
	return &pendingMainLLMCall{
		turnID:            s.continuation.turnID,
		turnEpoch:         s.continuation.turnEpoch,
		agentErrSourceID:  s.continuation.agentErrSourceID,
		planID:            s.planID,
		sessionEpoch:      s.target.sessionEpoch,
		continuation:      s.continuation.kind,
		oversizeSuspended: s.oversizeSuspended,
	}
}

func (a *MainAgent) currentCompactionPendingCall() *pendingMainLLMCall {
	return a.compactionState.pendingCall()
}

func (a *MainAgent) resetCompactionState() {
	a.compactionState = compactionState{}
}

func (a *MainAgent) finishCompactionState() (pending *pendingMainLLMCall, discard bool) {
	pending = a.currentCompactionPendingCall()
	discard = a.compactionState.discard
	if a.compactionState.cancel != nil {
		a.compactionState.cancel()
	}
	a.resetCompactionState()
	if discard {
		pending = nil
	}
	return pending, discard
}

func (a *MainAgent) markCompactionDiscard() {
	if a.compactionState.running {
		a.compactionState.discard = true
	}
}

// IsCompactionRunning reports whether a compaction goroutine is currently
// in flight, or a draft is waiting to be applied at the continuation barrier.
// This is the public API for TUI to query compaction state.
func (a *MainAgent) IsCompactionRunning() bool {
	return a.compactionState.isRunning() || a.compactionState.readyDraft != nil
}

// CancelCompaction cancels the in-flight compaction goroutine. Returns true
// if there was a running compaction to cancel.
func (a *MainAgent) CancelCompaction() bool {
	if !a.compactionState.isRunning() && a.compactionState.readyDraft == nil {
		return false
	}
	a.markCompactionDiscard()
	// Clear the ready draft (waiting for barrier)
	a.compactionState.readyDraft = nil
	if a.compactionState.cancel != nil {
		a.compactionState.cancel()
	}
	// If only had readyDraft (no running goroutine), clear discard state
	if !a.compactionState.running {
		a.compactionState.discard = false
	}
	return true
}

type compactionFailure struct {
	planID         uint64
	target         compactionTarget
	err            error
	absHistoryPath string // For orphan cleanup on cancel/failure
}

func (a *MainAgent) currentTurnEpoch() uint64 {
	if a.turn == nil {
		return 0
	}
	return a.turn.Epoch
}

func (a *MainAgent) mainLLMToolDefinitions() []message.ToolDefinition {
	if frozen := a.frozenToolDefs.Load(); frozen != nil {
		return *frozen
	}
	visibleTools := a.mainVisibleLLMTools()
	return llmToolDefinitionsFromVisibleTools(visibleTools)
}

// freezeToolSurface captures the current visible tool definitions as the
// agent's frozen surface. Called once by ensureSessionBuilt after MCP tools
// (if any) are registered.
func (a *MainAgent) freezeToolSurface() {
	defs := llmToolDefinitionsFromVisibleTools(a.mainVisibleLLMTools())
	snapshot := append([]message.ToolDefinition(nil), defs...)
	a.frozenToolDefs.Store(&snapshot)
}

// clearFrozenToolSurface drops the frozen snapshot so the next
// ensureSessionBuilt re-captures it. Called on session-head resets.
func (a *MainAgent) clearFrozenToolSurface() {
	a.frozenToolDefs.Store(nil)
}

func (a *MainAgent) nextCompactionPlan() (uint64, compactionTarget) {
	a.nextCompactionPlanID++
	return a.nextCompactionPlanID, compactionTarget{
		turnID:       a.currentTurnID(),
		turnEpoch:    a.currentTurnEpoch(),
		sessionEpoch: a.sessionEpoch,
	}
}

func compactionDraftMatchesPending(draft *compactionDraft, pending *pendingMainLLMCall) bool {
	if draft == nil || pending == nil {
		return false
	}
	if pending.planID != 0 && draft.PlanID != pending.planID {
		return false
	}
	if pending.sessionEpoch != 0 && draft.Target.sessionEpoch != pending.sessionEpoch {
		return false
	}
	if pending.turnID != 0 && draft.Target.turnID != pending.turnID {
		return false
	}
	if pending.turnEpoch != 0 && draft.Target.turnEpoch != pending.turnEpoch {
		return false
	}
	return true
}

func compactionFailureMatchesPending(payload *compactionFailure, pending *pendingMainLLMCall) bool {
	if payload == nil || pending == nil {
		return false
	}
	if pending.planID != 0 && payload.planID != pending.planID {
		return false
	}
	if pending.sessionEpoch != 0 && payload.target.sessionEpoch != pending.sessionEpoch {
		return false
	}
	if pending.turnID != 0 && payload.target.turnID != pending.turnID {
		return false
	}
	if pending.turnEpoch != 0 && payload.target.turnEpoch != pending.turnEpoch {
		return false
	}
	return true
}

// beginMainLLMAfterPreparation runs the automatic durable-compaction gate
// (async worker) and eventually spawns the main LLM goroutine. Call only from
// the event-loop goroutine after any context mutations for this round
// (e.g. processPendingUserMessagesBeforeLLMInTurn).
func (a *MainAgent) beginMainLLMAfterPreparation(turnCtx context.Context, turnID uint64, agentErrSourceID string) {
	// Continuation barrier: apply any ready compaction draft first. When the
	// apply path resumes a saved continuation (handled=true), it owns control
	// flow from here; otherwise this fresh pre-request path should continue on
	// the compacted context. Failed applies fall back to idle rather than running
	// the same stale gate again.
	if a.compactionState.readyDraft != nil {
		applySucceeded, handled := a.applyReadyDraft()
		if handled {
			return
		}
		if !applySucceeded {
			a.emitActivity("main", ActivityIdle, "")
			a.emitInteractiveToTUI(a.parentCtx, IdleEvent{})
			a.drainPendingUserMessages()
			return
		}
	}

	snapshot := a.ctxMgr.Snapshot()
	if a.trySkipUsageDrivenCompactionAfterShrink(snapshot) {
		a.spawnMainLLMResponseGoroutine(turnCtx, turnID, snapshot, agentErrSourceID)
		return
	}
	trigger := a.compactionTriggerForMainLLM()
	if !trigger.needed() {
		a.spawnMainLLMResponseGoroutine(turnCtx, turnID, snapshot, agentErrSourceID)
		return
	}

	// Threshold crossed: start background compaction WITHOUT blocking the LLM
	// call.  The compaction runs asynchronously with a lightweight idle
	// continuation; the LLM call is spawned in parallel immediately below.
	// If the LLM response returns a context_length_exceeded error, the
	// existing handleCompactionOversizeSuspend path will suspend the turn
	// until the compaction draft is ready and applied.
	//
	// Guard: if a compaction is already running (e.g. started earlier in this
	// turn or inherited from a previous turn), do not start a second one;
	// just spawn the LLM call and let the oversize-suspend mechanism handle
	// any context_length_exceeded error that may arise.
	if a.IsCompactionRunning() {
		log.Debugf("beginMainLLMAfterPreparation: compaction already running, spawning LLM in parallel turn_id=%v", turnID)
		a.spawnMainLLMResponseGoroutine(turnCtx, turnID, snapshot, agentErrSourceID)
		return
	}
	a.fireBeforeCompressHook(snapshot, false)
	planID, target := a.nextCompactionPlan()
	target.turnID = turnID
	target.turnEpoch = a.currentTurnEpoch()
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, trigger, continuationPlan{
		// Threshold-based compaction is automatic: once the durable summary
		// applies we want the agent to keep working on the compacted context.
		// resumePendingMainLLMAfterCompaction's compactionResumeAutoContinue
		// branch is responsible for spawning the next LLM call when the turn
		// has already finished.
		kind:             compactionResumeAutoContinue,
		turnID:           turnID,
		turnEpoch:        target.turnEpoch,
		agentErrSourceID: agentErrSourceID,
	}, false)

	// Spawn LLM call in parallel with background compaction.
	// If the call hits oversize, handleCompactionOversizeSuspend will pause
	// the turn until the compaction draft is applied.
	a.spawnMainLLMResponseGoroutine(turnCtx, turnID, snapshot, agentErrSourceID)
}

func (a *MainAgent) spawnMainLLMResponseGoroutine(turnCtx context.Context, turnID uint64, messages []message.Message, agentErrSourceID string) {
	a.pendingLoopContinuation = nil
	a.outputWg.Add(1)
	go func() {
		defer a.outputWg.Done()
		resp, err := a.callLLM(turnCtx, messages)
		if err != nil {
			if turnCtx.Err() != nil {
				return
			}
			// If context length exceeded while compaction is running,
			// suspend this LLM call until compaction completes.
			if IsContextLengthExceededPendingCompaction(err) {
				a.sendEvent(Event{
					Type:   EventCompactionOversizeSuspend,
					TurnID: turnID,
					Payload: &pendingMainLLMCall{
						continuation:      compactionResumeMainLLM,
						turnID:            turnID,
						turnEpoch:         a.currentTurnEpoch(),
						sessionEpoch:      a.sessionEpoch,
						agentErrSourceID:  agentErrSourceID,
						oversizeSuspended: true,
					},
				})
				return
			}
			ev := Event{
				Type:    EventAgentError,
				TurnID:  turnID,
				Payload: err,
			}
			if agentErrSourceID != "" {
				ev.SourceID = agentErrSourceID
			}
			a.sendEvent(ev)
			return
		}
		payload := &LLMResponsePayload{
			Content:                   resp.Content,
			ThinkingBlocks:            resp.ThinkingBlocks,
			ToolCalls:                 resp.ToolCalls,
			StopReason:                resp.StopReason,
			ThinkingToolcallMarkerHit: resp.ThinkingToolcallMarkerHit,
			ReasoningContent:          resp.ReasoningContent,
			Usage:                     resp.Usage,
		}
		now := time.Now()
		a.recordToolTraceLLMResponseEventSent(payload, now)
		a.sendEvent(Event{
			Type:    EventLLMResponse,
			TurnID:  turnID,
			Payload: payload,
		})
	}()
}

func (a *MainAgent) handleCompactionReady(evt Event) {
	draft, ok := evt.Payload.(*compactionDraft)
	if !ok || draft == nil {
		log.Errorf("handleCompactionReady: invalid payload type=%v", fmt.Sprintf("%T", evt.Payload))
		a.resetCompactionState()
		return
	}

	pending := a.currentCompactionPendingCall()
	if !compactionDraftMatchesPending(draft, pending) {
		log.Debugf("ignoring stale compaction ready event draft_plan_id=%v pending_plan_id=%v draft_session_epoch=%v draft_turn=%v draft_turn_epoch=%v", draft.PlanID, func() uint64 {
			if pending == nil {
				return 0
			}
			return pending.planID
		}(), draft.Target.sessionEpoch, draft.Target.turnID, draft.Target.turnEpoch)
		return
	}

	discard := a.compactionState.discard
	// Clear cancel function before applying
	if a.compactionState.cancel != nil {
		a.compactionState.cancel()
	}
	a.compactionState.discard = false

	// Determine whether to apply now or defer to the continuation barrier.
	// Apply immediately when there is no active turn or an oversize LLM call is
	// suspended; otherwise defer until the active LLM/tool work reaches a barrier.
	asyncPath := draft.HeadSplit > 0 && a.compactionState.headSplit > 0
	turnActive := a.turn != nil
	canApplyNow := !turnActive || a.compactionState.oversizeSuspended

	if asyncPath && !canApplyNow {
		// Case C: Turn is active and no pending call means we're mid-LLM/tool.
		// Defer application to the next continuation barrier.
		a.compactionState.readyDraft = draft

		// Store pending call info for resume after application.
		// Do NOT overwrite an existing oversize-suspend / main_llm continuation
		// here: handleCompactionOversizeSuspend (and other paths that arm a
		// stronger resume kind) may have already set it and the pending
		// snapshot for this deferred branch is weaker. Preserving the stronger
		// kind ensures the suspended LLM call gets resumed after apply.
		if pending != nil {
			preserve := a.compactionState.oversizeSuspended ||
				a.compactionState.continuation.kind == compactionResumeMainLLM ||
				a.compactionState.continuation.kind == compactionResumeLengthRecovery
			if !preserve {
				a.compactionState.continuation = continuationPlan{
					kind:             pending.continuation,
					turnID:           pending.turnID,
					turnEpoch:        pending.turnEpoch,
					agentErrSourceID: pending.agentErrSourceID,
				}
			}
		}

		// Emit activity to show compaction is ready but waiting for barrier
		a.emitActivity("main", ActivityCompacting, "context")
		log.Infof("compaction ready, waiting for continuation barrier plan_id=%v turn_active=%v pending_llm_call=%v has_pending_user=%v", draft.PlanID, turnActive, pending != nil, len(a.pendingUserMessages) > 0)
		return
	}

	// Apply immediately
	applySucceeded := false
	if !discard {
		if err := a.applyCompactionDraft(draft); err != nil {
			class := classifyCompactionFailure(err)
			a.recordCompactionFailureAnalyticsEvent(err, class, "apply")
			a.noteCompactionFailure(err)
			log.Warnf("apply compaction draft failed error=%v", err)
			a.emitToTUI(ToastEvent{
				Message: fmt.Sprintf("Context compaction failed: %v", err),
				Level:   "warn",
			})
		} else {
			applySucceeded = true
		}
	} else {
		log.Infof("discarding ready compaction draft due to higher-priority queued work turn_id=%v instance=%v plan_id=%v", evt.TurnID, a.instanceID, draft.PlanID)
	}

	pending, _ = a.finishCompactionState()
	a.emitActivity("main", ActivityIdle, "")
	if pending == nil {
		a.drainPendingUserMessages()
		return
	}
	_ = a.resumePendingMainLLMAfterCompaction(pending, applySucceeded)
}

// applyReadyDraft applies the compaction draft that was waiting for the continuation barrier.
// This is called from barrier events (beginMainLLMAfterPreparation, IdleEvent).
func (a *MainAgent) applyReadyDraft() (applySucceeded bool, handledIdleBarrier bool) {
	if a.compactionState.readyDraft == nil {
		return false, false
	}

	draft := a.compactionState.readyDraft
	a.compactionState.readyDraft = nil // Clear before applying

	// Session switch check
	if draft.Target.sessionEpoch != a.sessionEpoch {
		log.Debug("compaction draft discarded due to session switch")
		return false, false
	}

	log.Infof("applying compaction draft at continuation barrier plan_id=%v has_pending_llm=%v", draft.PlanID, a.compactionState.continuation.kind != "")

	if err := a.applyCompactionDraft(draft); err != nil {
		class := classifyCompactionFailure(err)
		a.recordCompactionFailureAnalyticsEvent(err, class, "apply_barrier")
		a.noteCompactionFailure(err)
		log.Warnf("apply compaction draft at barrier failed error=%v", err)
		a.emitToTUI(ToastEvent{
			Message: fmt.Sprintf("Context compaction failed: %v", err),
			Level:   "warn",
		})
	} else {
		applySucceeded = true
	}

	// Capture pending call before clearing state.
	pendingCall := a.currentCompactionPendingCall()

	// Clear all compaction state, including running flag.
	// This is critical: without this, compactionState.running stays true
	// and blocks all future compaction scheduling.
	a.resetCompactionState()

	// Resume pending call, if any.
	if pendingCall != nil {
		handledIdleBarrier = a.resumePendingMainLLMAfterCompaction(pendingCall, applySucceeded)
	}

	log.Infof("compaction draft applied at barrier plan_id=%v apply_succeeded=%v handled_idle_barrier=%v", draft.PlanID, applySucceeded, handledIdleBarrier)

	return applySucceeded, handledIdleBarrier
}

// handleCompactionOversizeSuspend saves an LLM call that was suspended because
// context length was exceeded while compaction was running. The call is resumed
// after compaction applies.
func (a *MainAgent) handleCompactionOversizeSuspend(evt Event) {
	pending, ok := evt.Payload.(*pendingMainLLMCall)
	if !ok || pending == nil {
		log.Errorf("handleCompactionOversizeSuspend: invalid payload type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	log.Infof("LLM call suspended due to oversize while compaction running turn_id=%v continuation=%v", evt.TurnID, pending.continuation)
	// Store as the compaction continuation so it will be resumed after apply
	a.compactionState.continuation = continuationPlan{
		kind:             pending.continuation,
		turnID:           pending.turnID,
		turnEpoch:        pending.turnEpoch,
		agentErrSourceID: pending.agentErrSourceID,
	}
	a.compactionState.oversizeSuspended = true
}
func (a *MainAgent) resumePendingMainLLMAfterCompaction(pending *pendingMainLLMCall, recheckGate bool) (handledIdleBarrier bool) {
	if pending == nil {
		return false
	}
	if pending.sessionEpoch != a.sessionEpoch {
		return false
	}
	if pending.continuation == compactionResumeIdle {
		if a.turn != nil {
			return false
		}
		if recheckGate {
			a.maybeRunAutoCompaction()
			if a.IsCompactionRunning() {
				return true
			}
		}
		a.emitInteractiveToTUI(a.parentCtx, IdleEvent{})
		a.drainPendingUserMessages()
		return true
	}
	if pending.continuation == compactionResumeAutoContinue {
		// Automatic compaction just applied (or failed). Unlike the idle
		// continuation we want the agent to keep going, because auto
		// compaction was triggered by the agent itself with no fresh user
		// input queued to wake the loop on its own.
		if !recheckGate {
			// Compaction failed/cancelled: surface idle state and let the
			// user take over. Do not restart the loop on a failed snapshot.
			a.emitActivity("main", ActivityIdle, "")
			if a.turn == nil {
				a.emitInteractiveToTUI(a.parentCtx, IdleEvent{})
				a.drainPendingUserMessages()
				return true
			}
			return false
		}
		if a.turn != nil {
			// A parallel LLM/tool turn is still running; its own finalize
			// path (setIdleAndDrainPending) will invoke applyReadyDraft
			// and re-enter this function once turn becomes nil. Just
			// refresh the activity so the spinner reflects reality.
			a.emitActivity("main", ActivityIdle, "")
			return false
		}
		// Guard against competing automatic compaction re-arming.
		a.maybeRunAutoCompaction()
		if a.IsCompactionRunning() {
			return true
		}
		// Spawn a fresh turn on the compacted context so the model keeps
		// making progress. Mirrors handleContinueFromContext.
		if a.loopState.Enabled {
			a.loopState.State = LoopStateExecuting
			a.emitLoopStateChanged()
		}
		a.newTurn()
		a.processPendingUserMessagesBeforeLLMInTurn()
		if a.turn == nil {
			return true
		}
		turnID := a.turn.ID
		turnCtx := a.turn.Ctx
		a.beginMainLLMAfterPreparation(turnCtx, turnID, "")
		return true
	}
	if pending.continuation == compactionResumeLengthRecovery {
		if a.turn == nil || a.turn.ID != pending.turnID || a.turn.Epoch != pending.turnEpoch {
			return false
		}
		if pending.sessionEpoch != a.sessionEpoch {
			return false
		}
		if !recheckGate {
			// Compaction failed: abort with guidance instead of retrying recovery.
			log.Warnf("length recovery auto compaction failed; aborting turn turn_id=%v", pending.turnID)
			a.emitToTUI(ErrorEvent{
				Err: fmt.Errorf(
					"automatic context compaction failed during output-limit recovery; " +
						"the context may be too large for automatic recovery; " +
						"please start a new conversation or try /compact before continuing",
				),
			})
			a.emitToTUI(ToastEvent{
				Message: "Try /compact to compress context, or start a new session.",
				Level:   "warn",
			})
			a.discardSpeculativeStreamToolsAndClearToolTrace(a.turn)
			a.setIdleAndDrainPending()
			return true
		}
		// Keep length-recovery continuation aligned with regular main continuation:
		// merge deferred user input first so recovery request sees the same prompt surface.
		a.processPendingUserMessagesBeforeLLMInTurn()
		if a.turn == nil || a.turn.ID != pending.turnID || a.turn.Epoch != pending.turnEpoch {
			return false
		}
		if pending.sessionEpoch != a.sessionEpoch {
			return false
		}
		// Compaction succeeded: resume recovery request with single-tool constraint.
		a.beginLengthRecoveryRetry(a.turn.LastTruncatedToolName, pending.turnID, a.turn.Ctx)
		return true
	}
	if a.turn == nil || a.turn.ID != pending.turnID || a.turn.Epoch != pending.turnEpoch {
		return false
	}
	// Deferred events may have queued additional user input for the same turn.
	// Merge that input before deciding how to resume the main-agent continuation.
	a.processPendingUserMessagesBeforeLLMInTurn()
	a.prepareSubAgentMailboxBatchForTurnContinuation()
	if a.turn == nil || a.turn.ID != pending.turnID || a.turn.Epoch != pending.turnEpoch {
		return false
	}
	if pending.sessionEpoch != a.sessionEpoch {
		return false
	}
	if pending.continuation == compactionResumeIdle {
		if a.turn != nil {
			return false
		}
		a.emitInteractiveToTUI(a.parentCtx, IdleEvent{})
		a.drainPendingUserMessages()
		return true
	}
	if recheckGate {
		// Queued user input or reports can still change the prompt surface after a
		// successful compaction commit. Re-enter the same pre-request compaction gate so
		// any newly armed automatic request is still honored on resume.
		a.beginMainLLMAfterPreparation(a.turn.Ctx, pending.turnID, pending.agentErrSourceID)
		return true
	}
	// If compaction itself failed, do not immediately retry the same gate; resume
	// the pending continuation directly to avoid an infinite compaction loop.
	a.spawnMainLLMResponseGoroutine(a.turn.Ctx, pending.turnID, a.ctxMgr.Snapshot(), pending.agentErrSourceID)
	return true
}

func (a *MainAgent) handleCompactionFailed(evt Event) {
	var payload *compactionFailure
	switch p := evt.Payload.(type) {
	case *compactionFailure:
		payload = p
	case error:
		payload = &compactionFailure{err: p}
	}
	if payload == nil {
		log.Errorf("handleCompactionFailed: invalid payload type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}

	pending := a.currentCompactionPendingCall()
	if payload.planID != 0 && !compactionFailureMatchesPending(payload, pending) {
		log.Debugf("ignoring stale compaction failure event failure_plan_id=%v pending_plan_id=%v", payload.planID, func() uint64 {
			if pending == nil {
				return 0
			}
			return pending.planID
		}())
		return
	}

	// Check if this was a cancellation (ESC key)
	isCancellation := payload.err != nil && errors.Is(payload.err, context.Canceled)

	if payload.err != nil && !isCancellation {
		class := classifyCompactionFailure(payload.err)
		a.recordCompactionFailureAnalyticsEvent(payload.err, class, "async")
		a.noteCompactionFailure(payload.err)
		log.Warnf("async context compaction failed error=%v", payload.err)
		a.emitToTUI(ToastEvent{
			Message: fmt.Sprintf("Context compaction failed: %v", payload.err),
			Level:   "warn",
		})
		a.emitToTUI(CompactionStatusEvent{Status: "failed"})
	} else if isCancellation {
		log.Info("context compaction cancelled by user")
		a.emitToTUI(ToastEvent{
			Message: "Context compaction cancelled",
			Level:   "info",
		})
		a.emitToTUI(CompactionStatusEvent{Status: "cancelled"})
		// Clean up orphan history files for cancelled compaction
		if payload.absHistoryPath != "" {
			cleanupOrphanCompactionFiles(payload.absHistoryPath)
		}
		// Cancellation does NOT count as a failure for breaker purposes
	}

	pending, _ = a.finishCompactionState()
	a.emitActivity("main", ActivityIdle, "")
	if pending == nil {
		// Auto compaction (not during an LLM call): drain any pending user messages
		// to continue the conversation automatically.
		a.drainPendingUserMessages()
		return
	}
	_ = a.resumePendingMainLLMAfterCompaction(pending, false)
}
