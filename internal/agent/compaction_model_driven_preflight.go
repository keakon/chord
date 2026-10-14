package agent

import (
	"fmt"

	"github.com/keakon/chord/internal/agent/requestshape"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// modelDrivenPreflightStats carries the low-gain preflight estimates from the
// worker to the event loop so skip and applied lifecycle events can record
// them. Zero for drafts that never ran a preflight (e.g. "not enough history").
type modelDrivenPreflightStats struct {
	CurrentTokens   int
	ProjectedTokens int
	SavedTokens     int
	SavedRatioPct   int
	// CurrentBytes / ProjectedBytes are the raw byte sizes of the reduced
	// request surface on each side, using the same accounting as the calibrated
	// token estimator's denominator. They let telemetry report the request
	// surface size independently of the token estimate.
	CurrentBytes       int
	ProjectedBytes     int
	CheckpointBytes    int
	AnchorBytes        int
	HistoryMapBytes    int
	ContinuationTokens int
	// CurrentSource records which current-side baseline produced the preflight
	// estimate: "last_prepared" (the most recent request surface this turn
	// actually sent to the provider, reused when the head is unchanged) or
	// "scratch" (a full reduction re-scan). Telemetry compares the estimate
	// against the provider's own input usage to gauge both baselines.
	CurrentSource string
	// CacheRebuildCost is the prompt-cache rewrite cost of the projected
	// prefix for prompt-cache-capable sessions (rewritten-prefix tokens ×
	// amortized write−read multiplier). Telemetry only: the low-gain gate
	// compares raw surface savings and never subtracts this per-provider
	// billing weight.
	CacheRebuildCost int
}

// ------------------------------------------------------------- preflight ---

// modelDrivenCurrentSurfaceBaseline returns the prepared current-side message
// surface for the low-gain preflight plus a provenance label for telemetry.
// When the snapshot is still an append-only extension of the source messages
// of this turn's most recent sent request, that request's reduced prefix is
// reused (the provider already saw it, so the estimate is exact for the head)
// and the appended tail is added unreduced; otherwise the scratch reduction
// re-scans the whole snapshot as an approximation of the next real request.
func (a *MainAgent) modelDrivenCurrentSurfaceBaseline(bundle modelDrivenBarrierSnapshot, snapshot []message.Message) ([]message.Message, string) {
	if bundle.lastPreparedTurnID != 0 && len(bundle.lastPreparedSource) > 0 && len(bundle.lastPreparedPrefix) > 0 {
		if requestshape.ReusablePrefixLen(bundle.lastPreparedSource, snapshot) == len(bundle.lastPreparedSource) {
			out := cloneMessageSliceForRequestShape(bundle.lastPreparedPrefix)
			out = append(out, snapshot[len(bundle.lastPreparedSource):]...)
			return out, "last_prepared"
		}
	}
	return bundle.prepareReducedRequest(snapshot), "scratch"
}

// modelDrivenLowGainPreflight estimates the prepared request surface with and
// without the reset. The current side is the next real request's reduced
// message surface; the projected side is the complete checkpoint (wrapper,
// history map, anchors, evidence artifact, display marker and model-driven
// copy) plus the preserved live tail, plus a conservative fixed allowance for
// the request-local overlays a durable rewrite re-injects. Both sides carry
// the fixed system-prompt + tool-definition surface, so the ratio gate is
// measured against the full request. Uncertain post-reset costs are
// overestimated, never ignored, so a reset is only skipped for clearly
// insufficient gain.
func (a *MainAgent) modelDrivenLowGainPreflight(bundle modelDrivenBarrierSnapshot, headSplit int, snapshot []message.Message, builder *modelDrivenCheckpointBuilder) (string, bool, modelDrivenPreflightStats) {
	currentSurface, currentSource := a.modelDrivenCurrentSurfaceBaseline(bundle, snapshot)
	// Queued user messages are merged after reduction on the real request
	// path, so they append to the prepared surface on both sides.
	currentSurface = append(currentSurface, bundle.queuedUserMessages...)
	checkpointContent, preflight := builder.render("")
	preflight.CurrentSource = currentSource
	projected := []message.Message{{Role: message.RoleUser, Content: checkpointContent, IsCompactionSummary: true}}
	projected = append(projected, checkpointRetainedFailureRecords(snapshot[:headSplit])...)
	projected = append(projected, snapshot[headSplit:]...)
	projected = append(projected, bundle.queuedUserMessages...)

	currentTokens := bundle.estimateTokens(currentSurface) + bundle.fixedRequestTokens
	// The projected side pays the post-reset fixed surface: after apply, the
	// cache-friendly MCP mount is dropped for full top-level injection, so the
	// frozen/mounted subset used for the current side would understate it.
	projectedTokens := bundle.estimateTokens(projected) + bundle.postResetFixedRequestTokens + modelDrivenPostResetOverlayTokens
	saved := currentTokens - projectedTokens
	preflight.CurrentTokens = currentTokens
	preflight.ProjectedTokens = projectedTokens
	preflight.SavedTokens = saved
	preflight.CurrentBytes = ctxmgr.EstimateMessagesBytes(currentSurface) + bundle.fixedRequestTokens*3
	preflight.ProjectedBytes = ctxmgr.EstimateMessagesBytes(projected) + (bundle.postResetFixedRequestTokens+modelDrivenPostResetOverlayTokens)*3
	if currentTokens > 0 {
		preflight.SavedRatioPct = saved * 100 / currentTokens
	}
	// Prompt-cache rebuild cost: after apply the checkpoint prefix must be
	// cache-written once (write ≈ 1.25×) where a kept prefix would only have
	// been cache-read (≈ 0.1×). The rewritten prefix is the projected surface,
	// not the archived head, and the one-time delta is amortized over the
	// minimum apply interval. The cost is recorded on the preflight stats for
	// cost telemetry only; the low-gain gates below compare raw
	// surface savings, keeping pure surface semantics — the rebuild delta is a
	// per-provider billing weight, not a token the model attends to, and
	// whether it systematically eats the gains is answered by the telemetry
	// instead of being pre-decided inside the gate.
	var cacheRebuildCost int
	if bundle.promptCacheCapable && headSplit > 0 {
		cacheRebuildCost = modelDrivenCacheRebuildCost(projectedTokens)
	}
	preflight.CacheRebuildCost = cacheRebuildCost
	if reason, skip := modelDrivenLowGainCheck(currentTokens, saved); skip {
		return reason, true, preflight
	}
	return "", false, preflight
}

// modelDrivenLowGainCheck applies the fixed low-gain gates to a savings
// estimate and returns the skip reason when either gate fails. Both the
// pre-export preflight and the post-export re-check share it so the refined
// checkpoint cannot be approved on a stale estimate. The gates compare raw
// surface savings only: the prompt-cache rebuild cost is recorded on the
// preflight stats for telemetry but deliberately NOT subtracted here, so the
// gate keeps a pure surface semantics (the one-off cache rewrite of a kept
// prefix is a per-provider billing weight, not a token the model attends to);
// whether cache rebuilds systematically eat the gains is answered by the
// cost telemetry instead of being pre-decided inside the gate.
func modelDrivenLowGainCheck(currentTokens, saved int) (string, bool) {
	if saved < modelDrivenLowGainMinTokens || saved < int(float64(currentTokens)*modelDrivenLowGainMinRatio) {
		return fmt.Sprintf("projected savings %d tokens is below the low-gain gate (%d tokens and %d%% of the prepared surface)", saved, modelDrivenLowGainMinTokens, int(modelDrivenLowGainMinRatio*100)), true
	}
	return "", false
}

// modelDrivenCacheRebuildCost returns the amortized prompt-cache rebuild
// charge for a reset whose post-apply prefix is projectedTokens long: the
// write−read delta of cache-writing that prefix once, spread over the
// minModelDrivenApplyIntervalBatches requests that follow before another
// reset may apply.
func modelDrivenCacheRebuildCost(projectedTokens int) int {
	if projectedTokens <= 0 {
		return 0
	}
	return projectedTokens * modelDrivenCacheRebuildDeltaNumer / modelDrivenCacheRebuildDeltaDenom / minModelDrivenApplyIntervalBatches
}

const (
	// modelDrivenSkipReasonLowGain / modelDrivenSkipReasonInterval /
	// modelDrivenSkipReasonDuplicate are the
	// bound skip reasons recorded on the cooldown state.
	modelDrivenSkipReasonLowGain   = "low_gain"
	modelDrivenSkipReasonInterval  = "interval"
	modelDrivenSkipReasonDuplicate = "duplicate"
)

// modelDrivenPolicySkipRecordBatch returns the batch a pre-preflight policy
// skip records on the cooldown state. Only the interval reason records one:
// the cooldown state exists for the gates that read it, and the interval gate
// is the one re-decided from the apply anchor, so re-stamping it is harmless.
//
// A low-gain cooldown short-circuit records nothing (batch 0, which
// settleModelDrivenSkip ignores): the cooldown window is anchored on the
// ORIGINAL low-gain skip, and re-stamping the current batch on every
// cooled-down retry would turn a fixed window into a sliding one — a model
// that retries on every batch keeps current-last at 1, the window never
// expires, and the low-gain preflight would never run again.
//
// A duplicate verdict records nothing either, and for a sharper reason: no
// gate reads "duplicate" (modelDrivenIntervalCooldownVerdict binds its
// cooldown to low_gain alone, and the duplicate verdict re-derives itself from
// the checkpoint fingerprint), so recording it would only overwrite a live
// low-gain anchor with a reason nothing consults — cutting that cooldown
// window short and letting the expensive preflight run again early.
func modelDrivenPolicySkipRecordBatch(skipReason string, current uint64) uint64 {
	if skipReason != modelDrivenSkipReasonInterval {
		return 0
	}
	return current
}

// modelDrivenSkipDraft builds a policy skip draft carrying the verdict batch
// and reason so the event-loop settlement records exactly the numbers the
// worker decided on. Preflight stays nil for skips that never ran it (interval
// and cooldown short-circuits).
func modelDrivenSkipDraft(planID uint64, target compactionTarget, reason, skipReason string, batch uint64, preflight *modelDrivenPreflightStats) *compactionDraft {
	return &compactionDraft{
		Skip:                  true,
		InfoMessage:           "Context checkpoint skipped: " + reason,
		PlanID:                planID,
		Target:                target,
		ModelDrivenPreflight:  preflight,
		ModelDrivenSkipReason: skipReason,
		ModelDrivenSkipBatch:  batch,
	}
}

// modelDrivenIntervalCooldownVerdict decides the two pre-preflight policy
// gates from the barrier snapshot:
//   - the apply interval: fewer than
//     minModelDrivenApplyIntervalBatches since the last successful model-driven
//     apply skips without preflight. The current >= last guard prevents the
//     uint64 underflow of a restored session whose batch counter restarted
//     below the persisted anchor; such a stale anchor counts as satisfied
//     instead of throttling forever.
//   - the same-reason low-gain cooldown: within
//     minModelDrivenSkipCooldownBatches of a previous low-gain skip,
//     short-circuit without entering preflight (which would re-run
//     prepareMessagesForLLM for an outcome that cannot change).
//
// The interval gate runs first: it is deterministic and already skips without
// preflight, so an interval-rejected retry is re-gated by the interval itself,
// never cooled down (a parameter-corrected retry after an interval rejection
// must not be cooldown-blocked). The cooldown therefore binds only to the
// low-gain reason — the one whose repeated evaluation is expensive.
//
// It returns the skip reason, the bound skip reason, and whether to skip.
func (a *MainAgent) modelDrivenIntervalCooldownVerdict(bundle modelDrivenBarrierSnapshot) (string, string, bool) {
	current := bundle.currentRequestBatch
	// current < last means the recorded anchor comes from a counter the
	// current session no longer continues (a restored session whose batch
	// numbering restarted below the persisted anchor): the anchor is stale
	// and the interval counts as satisfied rather than blocking every
	// model-driven request until the counter catches up. current == last is
	// a genuine zero-batch gap and stays throttled.
	if bundle.lastModelDrivenApplyBatch > 0 && current >= bundle.lastModelDrivenApplyBatch &&
		current-bundle.lastModelDrivenApplyBatch < minModelDrivenApplyIntervalBatches {
		return fmt.Sprintf("the minimum %d-request-batch interval since the last applied context checkpoint has not elapsed", minModelDrivenApplyIntervalBatches), modelDrivenSkipReasonInterval, true
	}
	if bundle.lastModelDrivenSkipReason == modelDrivenSkipReasonLowGain &&
		current > bundle.lastModelDrivenSkipBatch &&
		current-bundle.lastModelDrivenSkipBatch < minModelDrivenSkipCooldownBatches {
		return "cooling down after a previous low_gain skip; wait a couple of model requests before retrying", modelDrivenSkipReasonLowGain, true
	}
	return "", "", false
}

func modelDrivenCheckpointDuplicate(bundle modelDrivenBarrierSnapshot, req *modelDrivenCheckpointRequest) bool {
	if req == nil {
		return false
	}
	// Queued user input is deliberately excluded from the runtime fingerprint
	// because it is merged after the checkpoint apply. It is nevertheless fresh
	// intent, so it must always make an otherwise identical request eligible.
	if len(bundle.queuedUserMessages) > 0 {
		return false
	}
	argsFingerprint := req.ArgsFingerprint
	if argsFingerprint == "" {
		argsFingerprint = modelDrivenArgsFingerprint(req.Args)
	}
	current := modelDrivenCheckpointFingerprint(argsFingerprint, bundle.runtimeStateFingerprint)
	return current != "" && current == bundle.lastModelDrivenCheckpointFingerprint &&
		!modelDrivenHasProgressSinceCheckpoint(bundle.snapshot)
}

func (a *MainAgent) modelDrivenCheckpointSkipVerdict(bundle modelDrivenBarrierSnapshot, req *modelDrivenCheckpointRequest) (string, string, bool) {
	if modelDrivenCheckpointDuplicate(bundle, req) {
		return "no new work or input since the last applied checkpoint, and the checkpoint arguments and runtime state are unchanged; continue the task or deliver the final response instead of retrying", modelDrivenSkipReasonDuplicate, true
	}
	return a.modelDrivenIntervalCooldownVerdict(bundle)
}
