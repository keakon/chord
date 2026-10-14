package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

func TestEnsureOversizeDrivenCompactionStartsMainResumeCompaction(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.ctxMgr = ctxmgr.NewManagerWithTokenBudgets(400000, 272000, 272000, 0, 0.8)
	if !a.ensureOversizeDrivenCompaction() {
		t.Fatal("expected oversize-driven compaction to start")
	}
	if a.turn.OversizeRecoveryCount != 1 {
		t.Fatalf("OversizeRecoveryCount = %d, want 1", a.turn.OversizeRecoveryCount)
	}
	if a.pendingCompactionResume == nil {
		t.Fatal("expected durable pending compaction resume to be armed")
	}
	if a.pendingCompactionResume.OversizeRetryCount != 1 {
		t.Fatalf("pending oversize retry count = %d, want 1", a.pendingCompactionResume.OversizeRetryCount)
	}
	if !a.IsCompactionRunning() {
		t.Fatal("expected compaction running state")
	}
	if a.compactionState.trigger != compactionTriggerOversize {
		t.Fatal("expected oversize_driven trigger for oversize compaction")
	}
	if a.compactionState.continuation.kind != compactionResumeMainLLM {
		t.Fatalf("continuation kind = %q, want %q", a.compactionState.continuation.kind, compactionResumeMainLLM)
	}
	foundToast := false
	for len(a.outputCh) > 0 {
		evt := <-a.outputCh
		if toast, ok := evt.(ToastEvent); ok && strings.Contains(toast.Message, "compacting context before retry") {
			foundToast = true
		}
	}
	if !foundToast {
		t.Fatal("expected oversize-driven compaction toast")
	}
}

func TestEnsureOversizeDrivenCompactionStopsAfterRetryLimit(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.newTurn("")
	a.ctxMgr = ctxmgr.NewManagerWithTokenBudgets(400000, 272000, 272000, 0, 0.8)
	a.turn.OversizeRecoveryCount = maxOversizeRecoveryAttempts
	if a.ensureOversizeDrivenCompaction() {
		t.Fatal("expected oversize-driven compaction to stop after retry limit")
	}
	if a.IsCompactionRunning() {
		t.Fatal("expected no compaction running state after retry limit")
	}
}

func TestCompactionFailureDoesNotRetrySameGate(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr.SetMaxTokens(1000)
	a.newTurn("")
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	turnEpoch := a.turn.Epoch
	a.pendingUserMessages = []pendingUserMessage{{Content: "queued after failure"}}
	a.startCompactionState(1, compactionTarget{turnID: turnID, turnEpoch: turnEpoch, sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeMainLLM, turnID: turnID, turnEpoch: turnEpoch, agentErrSourceID: "main"})

	a.handleCompactionFailed(Event{Type: EventCompactionFailed, TurnID: turnID, Payload: &compactionFailure{planID: 1, target: compactionTarget{turnID: turnID, turnEpoch: turnEpoch, sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})

	if a.IsCompactionRunning() {
		t.Fatal("expected same gate not to be retried immediately after compaction failure")
	}
	if a.turn == nil || a.turn.ID != turnID || a.turn.Epoch != turnEpoch {
		t.Fatalf("turn = %+v, want active original turn %d/%d", a.turn, turnID, turnEpoch)
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 after queued input merge", got)
	}
	msgs := a.ctxMgr.Snapshot()
	if len(msgs) == 0 || msgs[len(msgs)-1].Content != "queued after failure" {
		t.Fatalf("expected deferred user message to be merged into context after failure resume, got %+v", msgs)
	}
}

func TestUsageDrivenFailureCanRetryAcrossTurnsBeforeBreakerTrips(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(10000, 0.9)
	a.autoCompactRequested.Store(true)

	a.startCompactionState(1, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeIdle})
	a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: 1, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})

	if a.isUsageDrivenAutoCompactSuppressed() {
		t.Fatal("expected breaker to stay open before reaching failure threshold")
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("expected usage-driven auto compact request to remain armed before breaker trips")
	}
	if !a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected another turn to retry usage-driven compaction before breaker trips")
	}
}

func TestUsageDrivenCompactionEnabledInLoop(t *testing.T) {
	t.Parallel()
	a := &MainAgent{ctxMgr: ctxmgr.NewManager(10000, 0.9)}
	a.loopState.Enabled = true
	a.autoCompactRequested.Store(true)

	if !a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("loop mode should allow usage-driven durable compaction")
	}
	if !a.autoCompactRequested.Load() {
		t.Fatal("expected auto compact request to remain armed")
	}
}

func TestUsageDrivenFailureStopsRetriyingAfterBreakerTrips(t *testing.T) {
	t.Parallel()
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	a.ctxMgr = ctxmgr.NewManager(10000, 0.9)
	a.autoCompactRequested.Store(true)

	for planID := uint64(1); planID <= usageDrivenCompactionFailureThreshold; planID++ {
		a.startCompactionState(planID, compactionTarget{sessionEpoch: a.sessionEpoch}, compactionTriggerUsageDriven, continuationPlan{kind: compactionResumeIdle})
		a.handleCompactionFailed(Event{Type: EventCompactionFailed, Payload: &compactionFailure{planID: planID, target: compactionTarget{sessionEpoch: a.sessionEpoch}, err: fmt.Errorf("temporary compaction failure")}})
	}

	if !a.isUsageDrivenAutoCompactSuppressed() {
		t.Fatal("expected breaker to suppress usage-driven auto compaction after threshold")
	}
	if a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected usage-driven compaction to stop retrying after breaker trips")
	}
}

func TestLargeTranscriptDoesNotAutoCompactWithoutUsageSignal(t *testing.T) {
	t.Parallel()
	a := &MainAgent{ctxMgr: ctxmgr.NewManager(1000, 0.5)}
	a.ctxMgr.RestoreMessages([]message.Message{
		{Role: "user", Content: strings.Repeat("large prompt ", 400)},
		{Role: "assistant", Content: strings.Repeat("large answer ", 400)},
		{Role: "user", Content: strings.Repeat("follow-up ", 400)},
	})

	if a.shouldDurableCompactBeforeMainLLM() {
		t.Fatal("expected automatic compaction to require a usage-driven signal even for a large transcript")
	}
}
