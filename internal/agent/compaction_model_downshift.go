package agent

import (
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

// modelDownshiftNoticeText is the productized status line shown once a
// compaction triggered by a move to a smaller model window has applied. It is
// UI surface, not transcript content: nothing is appended to the conversation
// for it to describe.
const modelDownshiftNoticeText = "Context compacted to fit the new model's smaller window."

// modelDownshiftCrossing reports whether the current context already crosses
// the running model's auto-compaction line and an automatic compaction is not
// suppressed or already in flight. It is the shared trigger condition for the
// model-downshift compaction: moving to a smaller window makes this newly true
// at the switch moment, while plain usage-driven compaction otherwise waits for
// the next response finalize to arm.
func (a *MainAgent) modelDownshiftCrossing() bool {
	if a == nil || a.ctxMgr == nil || a.isUsageDrivenAutoCompactSuppressed() || a.IsCompactionRunning() {
		return false
	}
	return a.ctxMgr.AutoCompactDecision().ShouldCompact
}

// maybeRunModelDownshiftCompaction starts the model-downshift compaction from
// idle. Callers must have just switched the running model while the agent is
// idle (no active turn and no LLM request in flight) and applied the new
// model's threshold (applyModelCompactionConfig returned true). Starting here —
// at the switch event instead of the next request boundary — lets the
// compaction run while the user is still composing the next message. A switch
// that lands with a turn active is handled at the next pre-request gate by
// deferModelDownshiftCompactionAtGate instead.
func (a *MainAgent) maybeRunModelDownshiftCompaction() {
	if a == nil || a.turn != nil || a.mainLLMRequestInFlight.Load() {
		return
	}
	if !a.modelDownshiftCrossing() {
		return
	}
	a.startDownshiftCompaction()
	log.Infof("model downshift: starting context compaction from idle")
}

// deferModelDownshiftCompactionAtGate is the pre-request gate's model-downshift
// branch: the running model changed to a smaller window (pool switch or
// fallback) since the last gate and the current context crosses its line, so
// the request about to be prepared would go out over the line and could hit the
// new model's hard limit. Instead of racing that request against a parallel
// compaction, start the compaction now and defer the LLM round until the draft
// applies (compactionResumeMainLLM). Returns true when the round was deferred —
// the gate must not spawn the request. If the compaction fails, the round
// resumes on the old context, where the oversize-suspend safety net still
// applies if the provider rejects the request.
func (a *MainAgent) deferModelDownshiftCompactionAtGate(turnID uint64, agentErrSourceID string, snapshot []message.Message) bool {
	if a == nil || !a.modelDownshiftCrossing() {
		return false
	}
	a.startDownshiftCompactionWithContinuation(snapshot, turnID, agentErrSourceID)
	return true
}

// startDownshiftCompaction starts a model-downshift compaction with the
// standard automatic continuation: after the draft applies the agent stays idle
// unless fresh user input was queued meanwhile (compactionResumeAutoContinue).
func (a *MainAgent) startDownshiftCompaction() {
	snapshot := a.ctxMgr.Snapshot()
	a.fireBeforeCompressHook(snapshot, false)
	planID, target := a.nextCompactionPlan()
	a.scheduleCompactionAsync(snapshot, planID, target, compactionTriggerModelDownshift)
}

// startDownshiftCompactionWithContinuation starts a model-downshift compaction
// that defers the calling LLM round: the continuation keeps the round's request
// pending (compactionResumeMainLLM) so resumePendingMainLLMAfterCompaction
// re-enters the pre-request gate on the compacted context. downshiftSuspended
// marks the suspended round so handleCompactionReady applies the draft the
// moment it is ready instead of parking it at the continuation barrier.
func (a *MainAgent) startDownshiftCompactionWithContinuation(snapshot []message.Message, turnID uint64, agentErrSourceID string) {
	a.fireBeforeCompressHook(snapshot, false)
	planID, target := a.nextCompactionPlan()
	target.turnID = turnID
	target.turnEpoch = a.currentTurnEpoch()
	a.startCompactionAsyncWithContinuation(snapshot, planID, target, compactionTriggerModelDownshift, continuationPlan{
		kind:             compactionResumeMainLLM,
		turnID:           turnID,
		turnEpoch:        target.turnEpoch,
		agentErrSourceID: agentErrSourceID,
	}, false)
	a.compactionState.downshiftSuspended = true
	log.Infof("model downshift: deferred LLM round for context compaction turn_id=%v plan_id=%v", turnID, planID)
}

// emitModelDownshiftAppliedNotice surfaces the one-line productized notice when
// a model-downshift compaction applied successfully. Called from the apply path
// while compactionState still carries the trigger.
func (a *MainAgent) emitModelDownshiftAppliedNotice() {
	if a == nil || a.compactionState.trigger != compactionTriggerModelDownshift {
		return
	}
	a.emitToTUI(ToastEvent{Message: modelDownshiftNoticeText, Level: "info"})
}
