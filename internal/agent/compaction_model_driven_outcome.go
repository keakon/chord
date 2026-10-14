package agent

import (
	"fmt"
	"strconv"
	"strings"
)

// settleModelDrivenOutcome is the single settlement point for model-driven
// checkpoints that did not apply (skip / failure / cancel / discard). It
// records the lifecycle analytics event exactly once, emits the terminal TUI
// status exactly once with the model_driven trigger, and queues the transient
// continuation notice. It deliberately does NOT clear LastTokenUsage,
// autoCompactRequested, or the usage-driven failure state: the safety net
// stays armed and the next gate decides.
func (a *MainAgent) settleModelDrivenOutcome(status string, reason string, preflight *modelDrivenPreflightStats, eventPlanID uint64) {
	// A terminal outcome must never survive into a later recovery snapshot as
	// an accepted request. The lifecycle event has already captured the
	// request identity before this state is cleared.
	a.transitionModelDrivenProposal(status, reason)
	a.modelDrivenSkipNotice = strings.TrimSpace(reason)
	// The model already took its shot at a checkpoint: the threshold grace
	// (if any) ends here so the usage-driven safety net is not deferred again.
	a.exhaustCompactionGraceAfterModelDriven()
	// The settle answered the armed manual /compact intent without applying:
	// the imperative notice is withdrawn (it was answered), while the intent
	// stays armed so the manual worker restarts at the next gate — or at this
	// settle point, when the turn that hosted the checkpoint is already gone.
	a.withdrawManualCompactionNotice()
	diagnostic := map[string]string{
		"trigger": compactionTriggerModelDriven.analyticsName(),
		"reason":  a.modelDrivenSkipNotice,
	}
	if requestID := strings.TrimSpace(a.modelDrivenProposal.requestID); requestID != "" {
		diagnostic["request_id"] = requestID
	}
	if preflight != nil {
		diagnostic["current_tokens"] = strconv.Itoa(preflight.CurrentTokens)
		diagnostic["projected_tokens"] = strconv.Itoa(preflight.ProjectedTokens)
		diagnostic["saved_tokens"] = strconv.Itoa(preflight.SavedTokens)
		diagnostic["saved_ratio_pct"] = strconv.Itoa(preflight.SavedRatioPct)
		if preflight.CurrentSource != "" {
			diagnostic["current_source"] = preflight.CurrentSource
		}
		diagnostic["current_bytes"] = strconv.Itoa(preflight.CurrentBytes)
		diagnostic["projected_bytes"] = strconv.Itoa(preflight.ProjectedBytes)
		diagnostic["checkpoint_bytes"] = strconv.Itoa(preflight.CheckpointBytes)
		diagnostic["anchor_bytes"] = strconv.Itoa(preflight.AnchorBytes)
		diagnostic["history_map_bytes"] = strconv.Itoa(preflight.HistoryMapBytes)
		diagnostic["continuation_tokens"] = strconv.Itoa(preflight.ContinuationTokens)
		diagnostic["cache_rebuild_cost"] = strconv.Itoa(preflight.CacheRebuildCost)
	}
	a.recordCompactionLifecycleEvent(status, diagnostic)
	// The terminal TUI/control-plane event always carries the model_driven
	// trigger and the settled plan id. The plan id comes from the explicit
	// argument (the worker draft's, or the state's when the draft is generic):
	// a synchronous settle that never owned the compaction slot must
	// not emit an empty id, nor borrow the id of a running automatic
	// compaction it deliberately left untouched.
	if eventPlanID == 0 {
		eventPlanID = a.compactionState.planID
	}
	evt := CompactionStatusEvent{Status: status, Reason: a.modelDrivenSkipNotice, Trigger: compactionTriggerModelDriven.analyticsName()}
	if eventPlanID > 0 {
		evt.PlanID = strconv.FormatUint(eventPlanID, 10)
	}
	a.emitToTUI(evt)
}

// settleModelDrivenSkip records a model-driven policy skip (low-gain, apply
// interval, or same-reason cooldown) with the worker-computed preflight stats.
// The verdict batch and reason from the draft update the skip-cooldown state
// so a retry with the same reason short-circuits without re-running preflight;
// structural skips ("not enough history") carry no verdict and do not touch
// the cooldown state.
//
// draft must be non-nil: the only production caller reaches this through a
// branch that has already dereferenced draft.Skip. Half-guarding it (as this
// function once did) only hides a caller bug that the very next line would
// trip over anyway.
func (a *MainAgent) settleModelDrivenSkip(draft *compactionDraft) {
	if draft.ModelDrivenSkipReason != "" && draft.ModelDrivenSkipBatch > 0 {
		a.lastModelDrivenSkipBatch = draft.ModelDrivenSkipBatch
		a.lastModelDrivenSkipReason = draft.ModelDrivenSkipReason
	}
	reason := strings.TrimSpace(draft.InfoMessage)
	reason = strings.TrimPrefix(reason, "Context checkpoint skipped: ")
	if reason == "" {
		reason = "projected savings were too small"
	}
	a.settleModelDrivenOutcome(CompactionStatusSkipped, reason, draft.ModelDrivenPreflight, draft.PlanID)
}

// settleModelDrivenFailure records a failed model-driven compaction without
// clearing the usage-driven safety net.
func (a *MainAgent) settleModelDrivenFailure(err error) {
	reason := "the checkpoint request failed: " + shortCompactionFailureReason(err)
	a.settleModelDrivenOutcome(CompactionStatusFailed, reason, nil, a.compactionState.planID)
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Model-driven context checkpoint failed: %v", err), Level: "warn"})
}

// settleModelDrivenCancelled records a user-cancelled or discarded
// model-driven checkpoint.
func (a *MainAgent) settleModelDrivenCancelled(reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "the checkpoint request was cancelled"
	}
	a.settleModelDrivenOutcome(CompactionStatusCancelled, reason, nil, a.compactionState.planID)
}

// appendModelDrivenContinuationNotice queues the model-driven continuation
// notice as a one-shot transient turn overlay. It is NOT appended to ctxMgr:
// an internal diagnostic must never become a durable user message that a later
// compaction could misread as the latest user request.
func (a *MainAgent) appendModelDrivenContinuationNotice() {
	reason := strings.TrimSpace(a.modelDrivenSkipNotice)
	a.modelDrivenSkipNotice = ""
	if reason == "" {
		reason = "projected savings were too small"
	}
	if !strings.HasSuffix(reason, ".") {
		reason += "."
	}
	a.pendingModelDrivenNotice = "Context checkpoint not applied: " + reason + " The session continues on the previous context."
	a.emitToTUI(ToastEvent{Message: "Context checkpoint not applied: " + strings.TrimSuffix(reason, "."), Level: "info"})
}
