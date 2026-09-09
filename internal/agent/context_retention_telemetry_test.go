package agent

import (
	"math"
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

func TestRetentionWindowSummaryRates(t *testing.T) {
	var totals retentionWindowTotals
	totals.add(retentionStatsFixture())
	totals.add(ContextReductionStats{
		OverCompression: map[string]int{
			contextReductionOverCompressionReread:                1,
			contextReductionOverCompressionRereadChangedRevision: 1,
		},
		ArchiveReads: 2,
	})

	summary := summarizeRetentionWindow(totals)
	if summary.Requests != 2 || summary.Rereads != 3 {
		t.Fatalf("summary requests/rereads = %v/%v, want 2/3", summary.Requests, summary.Rereads)
	}
	if math.Abs(summary.RereadsPerRequest-1.5) > 1e-9 {
		t.Fatalf("rereads per request = %v, want 1.5", summary.RereadsPerRequest)
	}
	// Request 1 carried same+changed rereads (1+1); request 2 added a changed
	// reread only: 3 revision-tagged rereads, 2 with a changed revision.
	if summary.RevisionKnownRereads != 3 || summary.ChangedRevisionRereads != 2 {
		t.Fatalf("revision rereads = %v/%v, want 3/2", summary.RevisionKnownRereads, summary.ChangedRevisionRereads)
	}
	if math.Abs(summary.ChangedRevisionRate-2.0/3.0) > 1e-9 {
		t.Fatalf("changed revision rate = %v, want 2/3", summary.ChangedRevisionRate)
	}
	if summary.ArchiveReads != 5 || summary.ArchiveReadFailures != 1 {
		t.Fatalf("archive reads = %v/%v, want 5/1", summary.ArchiveReads, summary.ArchiveReadFailures)
	}
	if math.Abs(summary.ArchiveFailureRate-0.2) > 1e-9 {
		t.Fatalf("archive failure rate = %v, want 0.2", summary.ArchiveFailureRate)
	}
	if math.Abs(summary.StaleEvidenceShare-0.375) > 1e-9 {
		t.Fatalf("stale evidence share = %v, want 0.375", summary.StaleEvidenceShare)
	}

	empty := summarizeRetentionWindow(retentionWindowTotals{})
	if empty.RereadsPerRequest != 0 || empty.ChangedRevisionRate != 0 || empty.ArchiveFailureRate != 0 || empty.StaleEvidenceShare != 0 {
		t.Fatalf("empty summary rates must be zero: %+v", empty)
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

	// Both layers accumulate across requests...
	input := a.currentRetentionPolicyInput()
	if input.Session.Requests != 2 || input.Window.Requests != 2 {
		t.Fatalf("requests session/window = %v/%v, want 2/2", input.Session.Requests, input.Window.Requests)
	}
	if input.Session.Rereads != 2 || input.Session.ArchiveReads != 5 {
		t.Fatalf("session rereads/archive = %v/%v, want 2/5", input.Session.Rereads, input.Session.ArchiveReads)
	}
	if input.Session.RevisionKnownRereads != 3 || input.Session.ChangedRevisionRereads != 2 {
		t.Fatalf("session revision rereads = %v/%v, want 3/2", input.Session.RevisionKnownRereads, input.Session.ChangedRevisionRereads)
	}

	// ...and survive resetContextReductionStats (what a compaction apply does
	// to the per-request stats).
	a.resetContextReductionStats()
	if got := a.currentRetentionPolicyInput().Session.Requests; got != 2 {
		t.Fatalf("session requests after resetContextReductionStats = %v, want 2", got)
	}

	// Taking the window (the applied-event path) publishes and clears only the
	// window layer.
	if window := a.takeWindowRetentionSignals(); window.Requests != 2 || window.RereadAfterReduction != 2 {
		t.Fatalf("taken window = %+v, want requests 2 rereads 2", window)
	}
	if got := a.currentRetentionPolicyInput(); got.Window.Requests != 0 || got.Session.Requests != 2 {
		t.Fatalf("after take window/session requests = %v/%v, want 0/2", got.Window.Requests, got.Session.Requests)
	}

	// A request after the apply starts a fresh window on top of the session.
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(3, nil, nil, nil, nil, 0, contextReductionPolicy{})
	if got := a.currentRetentionPolicyInput(); got.Window.Requests != 1 || got.Session.Requests != 3 {
		t.Fatalf("post-apply window/session requests = %v/%v, want 1/3", got.Window.Requests, got.Session.Requests)
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

	// The apply consumed the window but kept the session totals.
	if got := a.retentionSignals.window.Requests; got != 0 {
		t.Fatalf("window requests after apply = %v, want 0", got)
	}
	if got := a.retentionSignals.session.Requests; got != 2 {
		t.Fatalf("session requests after apply = %v, want 2", got)
	}
}

func TestSessionRetentionSignalsResetClearsBothLayers(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})

	a.resetSessionRetentionSignals()
	input := a.currentRetentionPolicyInput()
	if input.Session.Requests != 0 || input.Window.Requests != 0 {
		t.Fatalf("session reset left requests session/window = %v/%v, want 0/0", input.Session.Requests, input.Window.Requests)
	}
}

func TestActivateLoadedSessionRestartsRetentionSignals(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	// Session A accumulated signal totals across prepared requests.
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(1, nil, nil, nil, nil, 0, contextReductionPolicy{})
	a.setContextReductionStats(retentionStatsFixture())
	a.rememberPreparedLLMRequest(2, nil, nil, nil, nil, 0, contextReductionPolicy{})
	if got := a.currentRetentionPolicyInput().Session.Requests; got != 2 {
		t.Fatalf("session requests before activation = %v, want 2", got)
	}

	// Activating a loaded session is the /resume boundary: both aggregator
	// layers restart at zero, exactly like the /new reset path.
	a.activateLoadedSession(&loadedSessionState{SessionPath: a.sessionDir})

	input := a.currentRetentionPolicyInput()
	if input.Session.Requests != 0 || input.Window.Requests != 0 {
		t.Fatalf("retention signals after session activation = session %v window %v requests, want 0/0", input.Session.Requests, input.Window.Requests)
	}
}

func TestRetentionPolicyEligibilityIsConservative(t *testing.T) {
	input := retentionPolicyInput{Session: retentionSignalSummary{Requests: 20, ReducedToolResults: 1}}
	if !retentionPolicyEligibility(input) {
		t.Fatal("stable low-risk observations should be eligible for evaluation")
	}
	input.Session.Rereads = 1
	if retentionPolicyEligibility(input) {
		t.Fatal("reread evidence must block policy eligibility")
	}
	input.Session.Rereads = 0
	input.Session.ArchiveFailureRate = 0.01
	if retentionPolicyEligibility(input) {
		t.Fatal("archive failures must block policy eligibility")
	}
}
