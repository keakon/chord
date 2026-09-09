package agent

import (
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/message"
)

func retentionStatsFixture() ContextReductionStats {
	return ContextReductionStats{
		Messages: 4,
		OverCompression: map[string]int{
			contextReductionOverCompressionReread:                2,
			contextReductionOverCompressionRereadSameRevision:    1,
			contextReductionOverCompressionRereadChangedRevision: 1,
		},
		ArchiveReads:        3,
		ArchiveReadFailures: 1,
		EvidenceCurrent:     5,
		EvidenceStale:       2,
		EvidenceSuperseded:  1,
	}
}

func TestRetentionWindowTotalsAddAccumulates(t *testing.T) {
	var totals retentionWindowTotals
	totals.add(retentionStatsFixture())
	// A second request with no maps must only move the request/reduction
	// counters, proving nil-map reads are safe and signal-less requests stay
	// signal-less.
	totals.add(ContextReductionStats{Messages: 2})

	want := retentionWindowTotals{
		Requests:              2,
		ReducedToolResults:    6,
		RereadAfterReduction:  2,
		RereadSameRevision:    1,
		RereadChangedRevision: 1,
		ArchiveReads:          3,
		ArchiveReadFailures:   1,
		EvidenceCurrent:       5,
		EvidenceStale:         2,
		EvidenceSuperseded:    1,
	}
	if totals != want {
		t.Fatalf("totals = %+v, want %+v", totals, want)
	}
}

// TestRetentionRequestsCountExactlyOncePerRequest pins the single-count
// denominator of the retention statistics: one prepared request increments
// the Requests counter of each aggregator layer by exactly one, no matter how
// many retention signals the request carried. The reread rates divide by this
// counter, so a request that double-counts would silently halve them.
func TestRetentionRequestsCountExactlyOncePerRequest(t *testing.T) {
	var totals retentionWindowTotals
	totals.add(retentionStatsFixture())
	if totals.Requests != 1 {
		t.Fatalf("one request counted %d times, want exactly 1", totals.Requests)
	}

	// The real per-request finalization path counts each prepared request once.
	a := newTestMainAgent(t, t.TempDir())
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})
	if a.retentionSignals.Requests != 1 {
		t.Fatalf("single request window requests = %v, want 1", a.retentionSignals.Requests)
	}
}

func TestAppendWindowRetentionSignalsDiagnostic(t *testing.T) {
	var totals retentionWindowTotals
	totals.add(retentionStatsFixture())
	totals.add(retentionStatsFixture())

	diagnostic := map[string]string{}
	appendWindowRetentionSignalsDiagnostic(diagnostic, totals)
	want := map[string]string{
		"window_requests":                 "2",
		"window_reduced_messages":         "8",
		"window_rereads":                  "4",
		"window_rereads_same_revision":    "2",
		"window_rereads_changed_revision": "2",
		"window_research":                 "0",
		"window_archive_reads":            "6",
		"window_archive_read_failures":    "2",
		"window_evidence_current":         "10",
		"window_evidence_stale":           "4",
		"window_evidence_superseded":      "2",
		"window_rereads_changed_pct":      "50",
		"window_archive_failure_pct":      "33",
	}
	for key, value := range want {
		if diagnostic[key] != value {
			t.Fatalf("diagnostic[%q] = %q, want %q (diagnostic=%+v)", key, diagnostic[key], value, diagnostic)
		}
	}

	// A signal-less window keeps the stable key set and omits the pct keys.
	empty := retentionWindowTotals{}
	emptyDiag := map[string]string{}
	appendWindowRetentionSignalsDiagnostic(emptyDiag, empty)
	if emptyDiag["window_requests"] != "0" {
		t.Fatalf("empty window_requests = %q, want 0", emptyDiag["window_requests"])
	}
	if _, ok := emptyDiag["window_rereads_changed_pct"]; ok {
		t.Fatalf("pct key must be omitted without a denominator: %+v", emptyDiag)
	}
}

func TestRetentionSignalsAccumulateAcrossRequestsAndApply(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Each production request prepares once: rememberPreparedLLMRequest is
	// that single per-request finalization point, so driving the merge through
	// it mirrors the real request path.
	first := retentionStatsFixture()
	a.setContextReductionStats(first)
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})
	second := ContextReductionStats{
		OverCompression: map[string]int{
			contextReductionOverCompressionRereadChangedRevision: 1,
		},
		ArchiveReads: 2,
	}
	a.setContextReductionStats(second)
	a.rememberPreparedLLMRequest(2, nil, nil, nil, nil, 0, contextReductionPolicy{})

	// The window totals accumulate across requests.
	if a.retentionSignals.Requests != 2 {
		t.Fatalf("window requests = %v, want 2", a.retentionSignals.Requests)
	}
	if a.retentionSignals.RereadAfterReduction != 2 || a.retentionSignals.ArchiveReads != 5 {
		t.Fatalf("window rereads/archive = %v/%v, want 2/5", a.retentionSignals.RereadAfterReduction, a.retentionSignals.ArchiveReads)
	}

	// ...and survive resetContextReductionStats (what a compaction apply does
	// to the per-request stats).
	a.resetContextReductionStats()
	if got := a.retentionSignals.Requests; got != 2 {
		t.Fatalf("window requests after resetContextReductionStats = %v, want 2", got)
	}

	// Taking the window (the applied-event path) publishes and clears it.
	if window := a.takeWindowRetentionSignals(); window.Requests != 2 || window.RereadAfterReduction != 2 {
		t.Fatalf("taken window = %+v, want requests 2 rereads 2", window)
	}
	if a.retentionSignals.Requests != 0 {
		t.Fatalf("window requests after take = %v, want 0", a.retentionSignals.Requests)
	}

	// A request after the apply starts a fresh window.
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(3, nil, nil, nil, nil, 0, contextReductionPolicy{})
	if a.retentionSignals.Requests != 1 {
		t.Fatalf("post-apply window requests = %v, want 1", a.retentionSignals.Requests)
	}
}

func TestCompactionAppliedEventCarriesWindowRetentionSignals(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	var events []analytics.UsageEvent
	a.SetUsageEventSink(func(event analytics.UsageEvent) { events = append(events, event) })

	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(2, nil, nil, nil, nil, 0, contextReductionPolicy{})

	a.recordCompactionAppliedAnalyticsEvent(&compactionDraft{
		PlanID: 7,
		Target: compactionTarget{turnID: 11},
	}, 3, []message.Message{{Role: message.RoleUser, Content: "kept"}})

	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Purpose != compactionLifecycleAnalyticsPurpose || event.Diagnostic["stage"] != "applied" {
		t.Fatalf("event purpose/stage = %q/%q, want applied lifecycle", event.Purpose, event.Diagnostic["stage"])
	}
	want := map[string]string{
		"window_requests":                 "2",
		"window_rereads":                  "4",
		"window_rereads_same_revision":    "2",
		"window_rereads_changed_revision": "2",
		"window_archive_reads":            "6",
		"window_archive_read_failures":    "2",
		"window_evidence_stale":           "4",
	}
	for key, value := range want {
		if event.Diagnostic[key] != value {
			t.Fatalf("event diagnostic[%q] = %q, want %q (event=%+v)", key, event.Diagnostic[key], value, event)
		}
	}

	// The apply consumed the window totals.
	if got := a.retentionSignals.Requests; got != 0 {
		t.Fatalf("window requests after apply = %v, want 0", got)
	}
}

func TestSessionRetentionSignalsResetClearsWindow(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})

	a.resetSessionRetentionSignals()
	if a.retentionSignals.Requests != 0 {
		t.Fatalf("session reset left window requests = %v, want 0", a.retentionSignals.Requests)
	}
}

func TestActivateLoadedSessionRestartsRetentionSignals(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Session A accumulated signal totals across prepared requests.
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(2, nil, nil, nil, nil, 0, contextReductionPolicy{})
	if got := a.retentionSignals.Requests; got != 2 {
		t.Fatalf("window requests before activation = %v, want 2", got)
	}

	// Activating a loaded session is the /resume boundary: the window signal
	// totals restart at zero, exactly like the /new reset path.
	a.activateLoadedSession(&loadedSessionState{SessionPath: a.sessionDir})

	if a.retentionSignals.Requests != 0 {
		t.Fatalf("retention signals after session activation = %v requests, want 0", a.retentionSignals.Requests)
	}
}
