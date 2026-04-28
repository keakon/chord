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
	a.ctxMgr = ctxmgr.NewManager(10000, true, 0.9)
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

func TestAutomaticCompactionIgnoresPromptSizeWithoutUsageSignal(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(1000, true, 0.5)
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
}

func TestUsageDrivenBreakerResetsAfterSkipCompaction(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
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
