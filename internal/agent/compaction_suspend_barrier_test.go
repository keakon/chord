package agent

import (
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
)

// newSuspendBarrierTestAgent builds an agent with a live turn and a running
// compaction, the state the oversize suspension handler expects.
func newSuspendBarrierTestAgent(t *testing.T, planID uint64, continuation continuationPlan) *MainAgent {
	t.Helper()
	a := newReadyTestMainAgent(t)
	a.globalConfig = &config.Config{
		Context: config.ContextConfig{
			Compaction: config.CompactionConfig{Threshold: 0.8},
		},
	}
	a.ctxMgr = ctxmgr.NewManagerWithInputBudget(1000, 1000, 0, 0.8)
	a.ctxMgr.Append(message.Message{Role: message.RoleUser, Content: "continue the task"})
	a.ctxMgr.UpdateFromUsage(message.TokenUsage{InputTokens: 900})
	a.newTurn()
	a.started.Store(true)

	a.startCompactionState(planID, compactionTarget{
		sessionEpoch: a.sessionEpoch,
		turnID:       a.turn.ID,
		turnEpoch:    a.turn.Epoch,
	}, compactionTriggerUsageDriven, continuation)
	t.Cleanup(func() {
		if a.IsCompactionRunning() {
			a.handleCompactionCancel()
		}
		a.compactionWg.Wait()
	})
	return a
}

// parkReadyDraftForTest puts a skip draft in the slot handleCompactionReady
// uses when the turn is still active, without running a real worker.
func parkReadyDraftForTest(a *MainAgent, planID uint64) {
	a.compactionState.readyDraft = &compactionDraft{
		PlanID: planID,
		Skip:   true,
		Target: compactionTarget{
			sessionEpoch: a.sessionEpoch,
			turnID:       a.turn.ID,
			turnEpoch:    a.turn.Epoch,
		},
	}
}

// TestOversizeSuspendAppliesDraftParkedAtBarrier covers the hand-off an
// oversize suspension owes a draft which became ready while the turn was still
// active. handleCompactionReady parks such a draft instead of applying it, and
// the LLM goroutine that armed the suspension has already returned with the
// pending error, so it emits no further event. Without the suspension applying
// the parked draft, nothing ever reaches the draft again: the compaction slot
// stays claimed, the turn never settles and later user input queues behind it.
func TestOversizeSuspendAppliesDraftParkedAtBarrier(t *testing.T) {
	const planID = uint64(7312)
	a := newSuspendBarrierTestAgent(t, planID, continuationPlan{kind: compactionResumeAutoContinue})
	parkReadyDraftForTest(a, planID)

	a.handleCompactionOversizeSuspend(Event{
		Type:   EventCompactionOversizeSuspend,
		TurnID: a.turn.ID,
		Payload: &pendingMainLLMCall{
			continuation: compactionResumeMainLLM,
			turnID:       a.turn.ID,
			turnEpoch:    a.turn.Epoch,
			sessionEpoch: a.sessionEpoch,
			planID:       planID,
		},
	})

	if a.compactionState.readyDraft != nil {
		t.Fatal("draft parked at the barrier is still parked after the oversize suspension armed")
	}
	if a.IsCompactionRunning() {
		t.Fatal("compaction slot is still claimed after the parked draft was applied")
	}
}
