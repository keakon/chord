package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestBeginCompactionStateSeedsPendingAndFinishCancels(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	a.beginCompactionState(
		42,
		compactionTarget{turnID: 7, turnEpoch: 8, sessionEpoch: 9},
		compactionTriggerUsageDriven,
		continuationPlan{kind: compactionResumeMainLLM, turnID: 7, turnEpoch: 8, agentErrSourceID: "main"},
		3,
		cancel,
	)

	if !a.IsCompactionRunning() {
		t.Fatal("expected compaction to be running")
	}
	if a.compactionState.headSplit != 3 || !a.compactionState.trigger.isUsageDriven() {
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
		compactionTriggerManual,
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

func TestCancelCompactionQueuesStateMutationOnEventLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	a.beginCompactionState(
		12,
		compactionTarget{sessionEpoch: a.sessionEpoch},
		compactionTriggerManual,
		continuationPlan{kind: compactionResumeIdle},
		0,
		cancel,
	)

	if !a.CancelCompaction() {
		t.Fatal("CancelCompaction returned false for active compaction")
	}
	if ctx.Err() != nil {
		t.Fatalf("cancel ran outside event loop: %v", ctx.Err())
	}

	evt, err := a.nextEvent(context.Background())
	if err != nil {
		t.Fatalf("nextEvent: %v", err)
	}
	if evt.Type != EventCompactionCancel {
		t.Fatalf("event type = %q, want %q", evt.Type, EventCompactionCancel)
	}
	a.dispatch(evt)
	if ctx.Err() != context.Canceled {
		t.Fatalf("event-loop cancel did not run: %v", ctx.Err())
	}
}

// TestCancelCompactionOnLoopReadyDraftCarriesTrigger verifies that cancelling
// a compaction with a parked ready draft emits the terminal cancelled event
// with the compaction trigger, not an empty-trigger event.
func TestCancelCompactionOnLoopReadyDraftCarriesTrigger(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.startCompactionState(
		7,
		compactionTarget{sessionEpoch: a.sessionEpoch},
		compactionTriggerManual,
		continuationPlan{kind: compactionResumeIdle},
	)
	a.compactionState.readyDraft = &compactionDraft{
		PlanID:         7,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch},
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-7.md"),
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
	}

	if !a.cancelCompactionOnLoop() {
		t.Fatal("cancelCompactionOnLoop() = false, want true")
	}
	evt := <-a.outputCh
	status, ok := evt.(CompactionStatusEvent)
	if !ok {
		t.Fatalf("event type = %T, want CompactionStatusEvent", evt)
	}
	if status.Status != CompactionStatusCancelled {
		t.Fatalf("status = %q, want %q", status.Status, CompactionStatusCancelled)
	}
	if status.Trigger != "manual" {
		t.Fatalf("trigger = %q, want manual", status.Trigger)
	}
}

// TestApplyReadyDraftSessionSwitchCarriesTrigger verifies that discarding a
// draft on session switch emits the terminal cancelled event with the
// compaction trigger instead of an empty-trigger event.
func TestApplyReadyDraftSessionSwitchCarriesTrigger(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.startCompactionState(
		8,
		compactionTarget{sessionEpoch: a.sessionEpoch},
		compactionTriggerUsageDriven,
		continuationPlan{kind: compactionResumeIdle},
	)
	a.compactionState.readyDraft = &compactionDraft{
		PlanID:         8,
		Target:         compactionTarget{sessionEpoch: a.sessionEpoch + 1},
		AbsHistoryPath: filepath.Join(a.sessionDir, "history-8.md"),
		NewMessages:    []message.Message{{Role: "user", Content: "[Context Summary]", IsCompactionSummary: true}},
	}

	applySucceeded, _ := a.applyReadyDraft()
	if applySucceeded {
		t.Fatal("applyReadyDraft() succeeded on session switch, want discard")
	}
	evt := <-a.outputCh
	status, ok := evt.(CompactionStatusEvent)
	if !ok {
		t.Fatalf("event type = %T, want CompactionStatusEvent", evt)
	}
	if status.Status != CompactionStatusCancelled {
		t.Fatalf("status = %q, want %q", status.Status, CompactionStatusCancelled)
	}
	if status.Trigger != "usage_driven" {
		t.Fatalf("trigger = %q, want usage_driven", status.Trigger)
	}
}
