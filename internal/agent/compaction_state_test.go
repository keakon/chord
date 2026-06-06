package agent

import (
	"context"
	"testing"
)

func TestBeginCompactionStateSeedsPendingAndFinishCancels(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	a.beginCompactionState(
		42,
		compactionTarget{turnID: 7, turnEpoch: 8, sessionEpoch: 9},
		compactionTrigger{UsageDriven: true},
		continuationPlan{kind: compactionResumeMainLLM, turnID: 7, turnEpoch: 8, agentErrSourceID: "main"},
		3,
		cancel,
	)

	if !a.IsCompactionRunning() {
		t.Fatal("expected compaction to be running")
	}
	if a.compactionState.headSplit != 3 || !a.compactionState.trigger.UsageDriven {
		t.Fatalf("compaction state = %#v", a.compactionState)
	}
	pending := a.currentCompactionPendingCall()
	if pending == nil {
		t.Fatal("pending call is nil")
	}
	if pending.planID != 42 || pending.turnID != 7 || pending.turnEpoch != 8 || pending.sessionEpoch != 9 || pending.continuation != compactionResumeMainLLM {
		t.Fatalf("pending = %#v", pending)
	}

	finished, discard := a.finishCompactionState()
	if discard {
		t.Fatal("finishCompactionState discard = true, want false")
	}
	if finished == nil || finished.planID != 42 {
		t.Fatalf("finished pending = %#v", finished)
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("compaction cancel was not called, ctx err=%v", ctx.Err())
	}
	if a.IsCompactionRunning() {
		t.Fatal("compaction should not be running after finish")
	}
}

func TestFinishCompactionStateDropsDiscardedPending(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.beginCompactionState(
		11,
		compactionTarget{sessionEpoch: 1},
		compactionTrigger{Manual: true},
		continuationPlan{kind: compactionResumeIdle},
		0,
		nil,
	)
	a.markCompactionDiscard()

	pending, discard := a.finishCompactionState()
	if !discard {
		t.Fatal("finishCompactionState discard = false, want true")
	}
	if pending != nil {
		t.Fatalf("discarded pending = %#v, want nil", pending)
	}
}
