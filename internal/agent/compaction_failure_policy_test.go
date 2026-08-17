package agent

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

func TestUsageDrivenAutoCompactFailureBreakerSuppressesAfterThreshold(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(10000, 0.9)
	a.gitStatusInjected.Store(true)
	a.autoCompactRequested.Store(true)

	for planID := uint64(1); planID <= usageDrivenCompactionFailureThreshold; planID++ {
		a.startCompactionState(planID, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})
		a.handleCompactionFailed(Event{
			Type:    EventCompactionFailed,
			Payload: &compactionFailure{planID: planID, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: errors.New("compaction backend unavailable")},
		})
	}

	if !a.isUsageDrivenAutoCompactSuppressed() {
		t.Fatal("expected usage-driven auto compaction to be suppressed after repeated failures")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("expected usage-driven auto compact request to be cleared after breaker trips")
	}
	if got := a.autoCompactFailureState.ConsecutiveFailures; got != usageDrivenCompactionFailureThreshold {
		t.Fatalf("ConsecutiveFailures = %d, want %d", got, usageDrivenCompactionFailureThreshold)
	}

	messages := []message.Message{{Role: "user", Content: "short prompt"}}
	_ = messages
	if a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected suppressed usage-driven request to skip automatic compaction")
	}
}

func TestUsageDrivenBreakerSuppressesIdleAutoCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.autoCompactRequested.Store(true)
	a.autoCompactFailureState = autoCompactionFailureState{
		ConsecutiveFailures: usageDrivenCompactionFailureThreshold,
		SuppressedUntilTurn: a.nextTurnID + usageDrivenCompactionSuppressTurns,
	}

	a.maybeRunAutoCompaction()
	if a.IsCompactionRunning() {
		t.Fatal("suppressed usage-driven auto compaction should not schedule a compaction worker")
	}
}

func TestStopResponseDoesNotStartIdleAutoCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(10000, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 9000})
	a.autoCompactRequested.Store(true)
	a.newTurn()

	a.setIdleAndDrainPending()

	if a.IsCompactionRunning() {
		t.Fatal("stop response should not start automatic compaction while going idle")
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("usage-driven compaction request should remain armed for the next LLM request")
	}
}

func TestStopResponseKeepsExistingCompactionRunning(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn()
	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeAutoContinue})

	a.setIdleAndDrainPending()

	if !a.IsCompactionRunning() {
		t.Fatal("stop response should not cancel an automatic compaction already in progress")
	}
}

func TestIdleAutoCompactionClearsStaleRequestBelowCurrentThreshold(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(400000, 272000, 16000, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 50000})
	a.autoCompactRequested.Store(true)
	a.autoCompactFailureState = autoCompactionFailureState{
		ConsecutiveFailures: 1,
		LastFailureClass:    string(compactionFailureTransient),
		LastFailureReason:   "previous small-budget trigger",
	}

	a.maybeRunAutoCompaction()
	if a.IsCompactionRunning() {
		t.Fatal("stale usage-driven request below current threshold should not schedule compaction")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("stale usage-driven request should be cleared")
	}
	if got := a.autoCompactFailureState; got != (autoCompactionFailureState{}) {
		t.Fatalf("autoCompactFailureState = %+v, want zero value", got)
	}
}

func TestAutomaticCompactionIgnoresPromptSizeWithoutUsageSignal(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(1000, 0.5)
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: "user", Content: strings.Repeat("very large prompt ", 400)},
		{Role: "assistant", Content: strings.Repeat("very large answer ", 400)},
		{Role: "user", Content: strings.Repeat("next question ", 400)},
	})

	if a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected large transcripts to skip automatic compaction until provider usage arms it")
	}
}

func TestUsageDrivenBreakerResetsAfterSuccessfulCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(2000, 1200, 200, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 1000, CacheWriteTokens: 100})
	a.autoCompactRequested.Store(true)
	a.autoCompactFailureState = autoCompactionFailureState{
		ConsecutiveFailures: usageDrivenCompactionFailureThreshold,
		SuppressedUntilTurn: 99,
		LastFailureClass:    string(compactionFailureStructural),
		LastFailureReason:   "compaction failed",
	}

	err := a.applyCompactionDraft(&compactionDraft{
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
		Index:          1,
		AbsHistoryPath: "/tmp/history-1.md",
		RelHistoryPath: "history-1.md",
		SummaryMode:    "truncate_only",
		ModelRef:       "fallback",
		ArchivedCount:  4,
	})
	if err != nil {
		t.Fatalf("applyCompactionDraft: %v", err)
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("expected auto compact request to be cleared after successful compaction")
	}
	if got := a.autoCompactFailureState; got != (autoCompactionFailureState{}) {
		t.Fatalf("autoCompactFailureState = %+v, want zero value", got)
	}
	if got := a.ctxMgr.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens = %d, want 0 after successful compaction", got)
	}
	if got := a.ctxMgr.LastTotalContextTokens(); got != 0 {
		t.Fatalf("LastTotalContextTokens = %d, want 0 after successful compaction", got)
	}
	a.autoCompactRequested.Store(true)
	a.maybeRunAutoCompaction()
	if a.IsCompactionRunning() {
		t.Fatal("stale pre-compaction usage should not schedule another auto compaction")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("stale auto compact request should be cleared after token usage reset")
	}
}

func TestUsageDrivenBreakerResetsAfterSkipCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(2000, 1200, 200, 0.8)
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 1000, CacheWriteTokens: 100})
	a.autoCompactRequested.Store(true)
	a.autoCompactFailureState = autoCompactionFailureState{
		ConsecutiveFailures: usageDrivenCompactionFailureThreshold,
		SuppressedUntilTurn: 99,
		LastFailureClass:    string(compactionFailureUnknown),
		LastFailureReason:   "boom",
	}

	if err := a.applyCompactionDraft(&compactionDraft{Skip: true, InfoMessage: "nothing to compact"}); err != nil {
		t.Fatalf("applyCompactionDraft skip: %v", err)
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("expected auto compact request to be cleared after skip")
	}
	if got := a.autoCompactFailureState; got != (autoCompactionFailureState{}) {
		t.Fatalf("autoCompactFailureState = %+v, want zero value", got)
	}
	if got := a.ctxMgr.LastInputTokens(); got != 0 {
		t.Fatalf("LastInputTokens = %d, want 0 after skipped compaction", got)
	}
	if got := a.ctxMgr.LastTotalContextTokens(); got != 0 {
		t.Fatalf("LastTotalContextTokens = %d, want 0 after skipped compaction", got)
	}
	a.autoCompactRequested.Store(true)
	a.maybeRunAutoCompaction()
	if a.IsCompactionRunning() {
		t.Fatal("stale pre-compaction usage should not schedule another auto compaction after skip")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("stale auto compact request should be cleared after skipped compaction")
	}
}

func TestSessionSwitchResetsUsageDrivenAutoCompactionFailureState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.autoCompactRequested.Store(true)
	a.autoCompactFailureState = autoCompactionFailureState{
		ConsecutiveFailures: usageDrivenCompactionFailureThreshold,
		SuppressedUntilTurn: 99,
		LastFailureClass:    string(compactionFailureTransient),
		LastFailureReason:   "rate limited",
	}

	oldRecovery, turnCtx := a.prepareSessionSwitch()
	if oldRecovery == nil {
		t.Fatal("expected old recovery manager")
	}
	if turnCtx == nil {
		t.Fatal("expected non-nil turn context")
	}
	if a.autoCompactRequested.Load() {
		t.Fatal("expected session switch to clear usage-driven auto compact request")
	}
	if got := a.autoCompactFailureState; got != (autoCompactionFailureState{}) {
		t.Fatalf("autoCompactFailureState = %+v, want zero value", got)
	}
}

func TestClassifyCompactionFailureStructuralForPromptStillTooLarge(t *testing.T) {
	err := errors.New("compaction prompt still exceeds reserved context budget")
	if got := classifyCompactionFailure(err); got != compactionFailureStructural {
		t.Fatalf("classifyCompactionFailure = %q, want %q", got, compactionFailureStructural)
	}
}

func TestClassifyCompactionFailureStructuralForInputTooLarge(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", errCompactionInputTooLargeAfterTruncation)
	if got := classifyCompactionFailure(err); got != compactionFailureStructural {
		t.Fatalf("classifyCompactionFailure = %q, want %q", got, compactionFailureStructural)
	}
}

func TestClassifyCompactionFailureStructuralForCanonicalizedModelSwitchFactoryError(t *testing.T) {
	err := errors.New("prepare failed: model switch factory is not configured")
	if got := classifyCompactionFailure(err); got != compactionFailureStructural {
		t.Fatalf("classifyCompactionFailure = %q, want %q", got, compactionFailureStructural)
	}
}

func TestClassifyCompactionFailureStructuralForRequestShapeError(t *testing.T) {
	err := &llm.APIError{StatusCode: 400, Message: "invalid_request_error: missing required parameter: input"}
	if got := classifyCompactionFailure(err); got != compactionFailureStructural {
		t.Fatalf("classifyCompactionFailure = %q, want %q", got, compactionFailureStructural)
	}
}

func TestClassifyCompactionFailureTransientForAllKeysCooling(t *testing.T) {
	err := &llm.AllKeysCoolingError{RetryAfter: time.Second}
	if got := classifyCompactionFailure(err); got != compactionFailureTransient {
		t.Fatalf("classifyCompactionFailure = %q, want %q", got, compactionFailureTransient)
	}
}

func TestClassifyCompactionFailureTransientForAPIRateLimit(t *testing.T) {
	for _, err := range []error{
		&llm.APIError{StatusCode: 429, Message: "rate limited"},
		&llm.APIError{StatusCode: 529, Message: "overloaded"},
	} {
		if got := classifyCompactionFailure(err); got != compactionFailureTransient {
			t.Fatalf("classifyCompactionFailure(%v) = %q, want %q", err, got, compactionFailureTransient)
		}
	}
}

func TestClassifyCompactionFailureTransientForWatchdogTimeout(t *testing.T) {
	if got := classifyCompactionFailure(errCompactionWatchdog); got != compactionFailureTransient {
		t.Fatalf("classifyCompactionFailure = %q, want %q", got, compactionFailureTransient)
	}
}

// A failure event whose planID matches the running compaction must release the
// compaction state even when the continuation snapshot's turn fields have
// drifted from the goroutine's original target (oversize-suspend rewrites the
// continuation while the goroutine keeps its target). Treating that mismatch
// as a stale event left IsCompactionRunning() true forever, silently disabling
// every future automatic compaction in the session.
func TestCompactionFailureWithDriftedContinuationStillReleasesState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	target := compactionTarget{turnID: 7, turnEpoch: 1, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(4, target, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeAutoContinue, turnID: 7, turnEpoch: 1})
	// Simulate a later path rewriting the continuation to resume a newer turn.
	a.compactionState.continuation.turnID = 9
	a.compactionState.continuation.turnEpoch = 2

	a.handleCompactionFailed(Event{
		Type:    EventCompactionFailed,
		Payload: &compactionFailure{planID: 4, target: target, err: errors.New("compaction backend unavailable")},
	})

	if a.IsCompactionRunning() {
		t.Fatal("failure for the running plan must release the compaction state despite continuation drift")
	}
}

// A genuinely stale failure event (older planID than the running compaction)
// must still be ignored without touching the newer compaction's state.
func TestCompactionFailureFromOlderPlanKeepsRunningState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.startCompactionState(6, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})
	a.handleCompactionFailed(Event{
		Type:    EventCompactionFailed,
		Payload: &compactionFailure{planID: 5, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: errors.New("late failure from a superseded plan")},
	})

	if !a.IsCompactionRunning() {
		t.Fatal("stale failure from an older plan must not release the newer compaction's state")
	}
}

// The success path must survive the same continuation drift as the failure
// path: a ready draft whose planID matches the running compaction is applied
// even when oversize-suspend has rewritten the continuation's turn fields.
// Treating the drift as stale discarded the produced draft, leaked
// IsCompactionRunning() forever, and never resumed the suspended LLM call.
func TestCompactionReadyWithDriftedContinuationStillApplies(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	target := compactionTarget{turnID: 7, turnEpoch: 1, sessionEpoch: a.sessionEpoch}
	a.startCompactionState(4, target, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeAutoContinue, turnID: 7, turnEpoch: 1})
	// Simulate a later path rewriting the continuation to resume a newer turn.
	a.compactionState.continuation.turnID = 9
	a.compactionState.continuation.turnEpoch = 2

	a.handleCompactionReady(Event{
		Type: EventCompactionReady,
		Payload: &compactionDraft{
			PlanID:         4,
			Target:         target,
			NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]\nsummary"}},
			Index:          1,
			AbsHistoryPath: "/tmp/history-drift.md",
			RelHistoryPath: "history-drift.md",
			SummaryMode:    "truncate_only",
			ModelRef:       "fallback",
			ArchivedCount:  4,
		},
	})

	if a.IsCompactionRunning() {
		t.Fatal("ready draft for the running plan must release the compaction state despite continuation drift")
	}
	messages := a.ctxMgr.Snapshot()
	if len(messages) != 1 || messages[0].Content != "[Context Summary]\nsummary" {
		t.Fatalf("drifted ready draft must still be applied, got messages = %+v", messages)
	}
}

// A genuinely stale ready event (older planID than the running compaction)
// must still be discarded without applying its draft or touching the newer
// compaction's state.
func TestCompactionReadyFromOlderPlanKeepsRunningState(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.startCompactionState(6, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTrigger{UsageDriven: true}, continuationPlan{kind: compactionResumeIdle})
	a.handleCompactionReady(Event{
		Type: EventCompactionReady,
		Payload: &compactionDraft{
			PlanID:      5,
			Target:      compactionTarget{sessionEpoch: a.sessionEpoch},
			NewMessages: []message.Message{{Role: "user", Content: "[Context Summary]\nsuperseded"}},
		},
	})

	if !a.IsCompactionRunning() {
		t.Fatal("stale ready event from an older plan must not release the newer compaction's state")
	}
	if messages := a.ctxMgr.Snapshot(); len(messages) != 0 {
		t.Fatalf("stale ready draft must not be applied, got messages = %+v", messages)
	}
}
