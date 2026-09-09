package agent

import (
	"strconv"
)

// Compaction-window retention signal aggregation.
//
// ContextReductionStats are per-request and are zeroed by
// resetContextReductionStats on every compaction apply, so alone they cannot
// answer whether reduction repeatedly discards output the model later
// re-reads. retentionWindowTotals keeps one monotonic layer that survives
// those resets: the window totals since the last durable compaction apply,
// published on the applied lifecycle event and then cleared. Every session
// boundary (session switch, restore and /new) clears the window together with
// the usage tracker, so the layer never spans two runs.
//
// Units follow ContextReductionStats: the over-compression keys count re-issue
// incidents, archive reads count archive-addressed reads present in each
// request surface, and the evidence fields count the file observations each
// request saw, so the sums are in "per-request observed" units and the ratios
// they feed are window-internal.
//
// Nothing here changes retention behavior: valid-read protections stay
// conservative until this telemetry proves they can relax.

// retentionWindowTotals accumulates the retention-relevant half of
// ContextReductionStats over one compaction window (the span since the last
// durable apply, or since the session boundary when no apply ran yet).
type retentionWindowTotals struct {
	// Requests counts prepared main requests observed in the span. Production
	// requests prepare exactly once per LLM call, so it is the reread-rate
	// denominator.
	Requests int64
	// ReducedToolResults is the sum of per-request stats.Messages: outputs
	// newly reduced to a lossy form in the span.
	ReducedToolResults int64
	// RereadAfterReduction totals rereads of an output that reduction had
	// discarded (OverCompression reread_after_reduction). Same- and
	// changed-revision rereads are the read subset with a known revision on
	// both sides, so RereadSameRevision + RereadChangedRevision <=
	// RereadAfterReduction.
	RereadAfterReduction  int64
	RereadSameRevision    int64
	RereadChangedRevision int64
	// ResearchAfterReduction totals re-issued searches of discarded content
	// (research_after_reduction), the non-read-like reread shape.
	ResearchAfterReduction int64
	// ArchiveReads / ArchiveReadFailures count reads that followed a recovery
	// address and how often they failed. An address nobody can use is
	// indistinguishable from a dropped payload until these diverge.
	ArchiveReads        int64
	ArchiveReadFailures int64
	// EvidenceCurrent / EvidenceStale / EvidenceSuperseded cover the file
	// observations each request saw: reads still valid, reads whose file
	// changed, and reads superseded by a later observation.
	EvidenceCurrent    int64
	EvidenceStale      int64
	EvidenceSuperseded int64
}

// add folds one prepared request's final stats into the window totals.
// Reading from a nil OverCompression map yields zero, so signal-less requests
// only move the Requests and ReducedToolResults counters.
func (t *retentionWindowTotals) add(stats ContextReductionStats) {
	if t == nil {
		return
	}
	t.Requests++
	t.ReducedToolResults += int64(stats.Messages)
	t.RereadAfterReduction += int64(stats.OverCompression[contextReductionOverCompressionReread])
	t.RereadSameRevision += int64(stats.OverCompression[contextReductionOverCompressionRereadSameRevision])
	t.RereadChangedRevision += int64(stats.OverCompression[contextReductionOverCompressionRereadChangedRevision])
	t.ResearchAfterReduction += int64(stats.OverCompression[contextReductionOverCompressionResearch])
	t.ArchiveReads += int64(stats.ArchiveReads)
	t.ArchiveReadFailures += int64(stats.ArchiveReadFailures)
	t.EvidenceCurrent += int64(stats.EvidenceCurrent)
	t.EvidenceStale += int64(stats.EvidenceStale)
	t.EvidenceSuperseded += int64(stats.EvidenceSuperseded)
}

// mergeRequestRetentionSignalsLocked folds one prepared request's final
// per-request stats into the compaction-window totals. The caller must hold
// loopReductionMu; rememberPreparedLLMRequest is the single production call
// site (it runs once per prepared main request, on the LLM worker goroutine
// while the event loop may concurrently apply a compaction, hence the lock).
func (a *MainAgent) mergeRequestRetentionSignalsLocked(stats ContextReductionStats) {
	if a == nil {
		return
	}
	a.retentionSignals.add(stats)
}

// takeWindowRetentionSignals snapshots the compaction-window totals and starts
// a fresh window. Called once per durable compaction apply so the applied
// lifecycle event can publish the window's signals before per-request stats
// are reset.
func (a *MainAgent) takeWindowRetentionSignals() retentionWindowTotals {
	if a == nil {
		return retentionWindowTotals{}
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	window := a.retentionSignals
	a.retentionSignals = retentionWindowTotals{}
	return window
}

// resetSessionRetentionSignals clears the compaction-window signal totals.
// Every session boundary — session switch, restore and /new — resets the
// usage tracker together with the reduction caches, and the signal totals
// follow that lifecycle: they describe the current loaded session run, never
// a span across runs.
func (a *MainAgent) resetSessionRetentionSignals() {
	if a == nil {
		return
	}
	a.loopReductionMu.Lock()
	defer a.loopReductionMu.Unlock()
	a.retentionSignals = retentionWindowTotals{}
}

func int64Percent(num, den int64) int64 {
	if den <= 0 {
		return 0
	}
	return num * 100 / den
}

// appendWindowRetentionSignalsDiagnostic writes the window totals of one
// compaction window into an applied-lifecycle diagnostic. Keys are stable so
// downstream consumers can rely on their presence regardless of whether the
// window saw any signals.
func appendWindowRetentionSignalsDiagnostic(diagnostic map[string]string, totals retentionWindowTotals) {
	if diagnostic == nil {
		return
	}
	format := func(v int64) string { return strconv.FormatInt(v, 10) }
	diagnostic["window_requests"] = format(totals.Requests)
	diagnostic["window_reduced_messages"] = format(totals.ReducedToolResults)
	diagnostic["window_rereads"] = format(totals.RereadAfterReduction)
	diagnostic["window_rereads_same_revision"] = format(totals.RereadSameRevision)
	diagnostic["window_rereads_changed_revision"] = format(totals.RereadChangedRevision)
	diagnostic["window_research"] = format(totals.ResearchAfterReduction)
	diagnostic["window_archive_reads"] = format(totals.ArchiveReads)
	diagnostic["window_archive_read_failures"] = format(totals.ArchiveReadFailures)
	diagnostic["window_evidence_current"] = format(totals.EvidenceCurrent)
	diagnostic["window_evidence_stale"] = format(totals.EvidenceStale)
	diagnostic["window_evidence_superseded"] = format(totals.EvidenceSuperseded)
	if known := totals.RereadSameRevision + totals.RereadChangedRevision; known > 0 {
		diagnostic["window_rereads_changed_pct"] = format(int64Percent(totals.RereadChangedRevision, known))
	}
	if totals.ArchiveReads > 0 {
		diagnostic["window_archive_failure_pct"] = format(int64Percent(totals.ArchiveReadFailures, totals.ArchiveReads))
	}
}

// windowRetentionSignalsDebugLine renders the window totals for the applied
// debug log line, mirroring the diagnostic keys without the prefix.
func windowRetentionSignalsDebugLine(totals retentionWindowTotals) string {
	return "requests=" + strconv.FormatInt(totals.Requests, 10) +
		" reduced_messages=" + strconv.FormatInt(totals.ReducedToolResults, 10) +
		" rereads=" + strconv.FormatInt(totals.RereadAfterReduction, 10) +
		" rereads_same_revision=" + strconv.FormatInt(totals.RereadSameRevision, 10) +
		" rereads_changed_revision=" + strconv.FormatInt(totals.RereadChangedRevision, 10) +
		" research=" + strconv.FormatInt(totals.ResearchAfterReduction, 10) +
		" archive_reads=" + strconv.FormatInt(totals.ArchiveReads, 10) +
		" archive_read_failures=" + strconv.FormatInt(totals.ArchiveReadFailures, 10) +
		" evidence_current=" + strconv.FormatInt(totals.EvidenceCurrent, 10) +
		" evidence_stale=" + strconv.FormatInt(totals.EvidenceStale, 10) +
		" evidence_superseded=" + strconv.FormatInt(totals.EvidenceSuperseded, 10)
}
