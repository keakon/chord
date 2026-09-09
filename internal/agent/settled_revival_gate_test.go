package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests pin the settled-task revival gates: a terminal runtime (its
// settlement committed but not yet parked — park can be refused by persistence
// failures or queued input) must never be resurrected by a late or manual
// delivery that skipped the explicit new-attempt machinery (attempt bump plus
// resetForAttempt). Review finding R2-中1: the transition table explicitly
// allowed terminal -> Running, so a plain manual delivery flipped a settled
// record back to running and later completions collided with the immutable
// settlement.

func TestManualDeliveryToLiveTerminalWorkerRefusedWithoutNewAttempt(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	configureNestedDelegationTestRuntime(a, 2)
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const taskID = "task-settled-live"
	sub := newControllableTestSubAgent(t, a, taskID)
	sub.setState(SubAgentStateRunning, "working")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "finished", "task completed", nil); err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}
	if got := sub.State(); got != SubAgentStateCompleted {
		t.Fatalf("sub.State() after commit = %q, want completed", got)
	}

	if _, _, err := a.deliverManualMessageToSubAgent(sub, "late follow-up", "follow_up"); err == nil {
		t.Fatal("manual delivery to a live settled worker succeeded; it must be refused without a new attempt")
	}
	if got := sub.State(); got != SubAgentStateCompleted {
		t.Fatalf("sub.State() = %q, want completed (runtime was resurrected)", got)
	}
	if rec := a.taskRecordByTaskID(taskID); rec == nil || rec.State != string(SubAgentStateCompleted) || rec.Attempt != 1 {
		t.Fatalf("record after refused delivery = %#v, want untouched completed attempt 1", rec)
	}
}

func TestSendUserMessageToCancelledLiveWorkerIsRefused(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	configureNestedDelegationTestRuntime(a, 2)
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const taskID = "task-cancelled-live"
	sub := newControllableTestSubAgent(t, a, taskID)
	sub.setState(SubAgentStateRunning, "working")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCancelled, "stopped by user", "stopped by user", nil); err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}
	a.SwitchFocus(sub.instanceID)

	a.SendUserMessage("late reply after cancel")

	if got := sub.State(); got != SubAgentStateCancelled {
		t.Fatalf("sub.State() = %q, want cancelled (a cancel must not be undone by a late message)", got)
	}
	select {
	case <-sub.inputCh:
		t.Fatal("message was queued for a cancelled task")
	default:
	}
}

func TestFocusedMessageToLiveCompletedWorkerStartsNewAttempt(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	configureNestedDelegationTestRuntime(a, 2)
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const taskID = "task-follow-up"
	sub := newControllableTestSubAgent(t, a, taskID)
	sub.setState(SubAgentStateRunning, "working")
	if _, _, err := a.commitTerminalTask(sub, SubAgentStateCompleted, "first pass done", "task completed", nil); err != nil {
		t.Fatalf("commitTerminalTask: %v", err)
	}
	a.SwitchFocus(sub.instanceID)

	a.SendUserMessage("redo the edge cases")

	if got := sub.State(); got != SubAgentStateRunning {
		t.Fatalf("sub.State() = %q, want running on a new attempt", got)
	}
	rec := a.taskRecordByTaskID(taskID)
	if rec == nil || rec.Attempt != 2 {
		t.Fatalf("record after focused follow-up = %#v, want a bumped attempt 2 (not a revival of attempt 1)", rec)
	}
	select {
	case msg := <-sub.inputCh:
		if text := pendingUserMessageText(msg); text != "[follow_up] redo the edge cases" {
			t.Fatalf("queued message = %q, want %q", text, "[follow_up] redo the edge cases")
		}
	default:
		t.Fatal("expected the new attempt to receive the follow-up input")
	}
}

// TestLiveWaitingMainExpiryBacksOffWhenWorkerReactivatedFirst pins the expiry
// side of the reactivation race (review finding R2-中2): when a manual reply
// wakes a WaitingMain worker before the expiry sweep commits, the guarded live
// settle must back off instead of cancelling the freshly resumed attempt and
// reporting a phantom expiry.
func TestLiveWaitingMainExpiryBacksOffWhenWorkerReactivatedFirst(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	configureNestedDelegationTestRuntime(a, 2)
	const taskID = "task-expiry-race"
	sub := newControllableTestSubAgent(t, a, taskID)
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)
	// The reactivation wins the race: the worker is already running again when
	// the expiry settle runs.
	a.markSubAgentReactivated(sub, "reply delivered")
	if got := sub.State(); got != SubAgentStateRunning {
		t.Fatalf("sub.State() after reactivation = %q, want running", got)
	}

	reason := waitingMainExpiryClosedReasonPrefix + " (no reply within the wait limit)"
	if a.settleLiveWaitingMainExpiry(sub, reason) {
		t.Fatal("live expiry committed for a worker that was already reactivated")
	}
	if got := sub.State(); got != SubAgentStateRunning {
		t.Fatalf("sub.State() after refused expiry = %q, want running", got)
	}
	if rec := a.taskRecordByTaskID(taskID); rec != nil && rec.State != string(SubAgentStateRunning) {
		t.Fatalf("record after refused expiry = %#v, want the running attempt untouched", rec)
	}
}

// TestLiveWaitingMainExpiryCommittedWhenStillWaiting confirms the same guarded
// settle still commits when the worker has not been reactivated.
func TestLiveWaitingMainExpiryCommittedWhenStillWaiting(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	configureNestedDelegationTestRuntime(a, 2)
	if err := os.MkdirAll(filepath.Join(a.sessionDir, "subagents"), 0o755); err != nil {
		t.Fatalf("MkdirAll(subagents): %v", err)
	}
	const taskID = "task-expiry-plain"
	sub := newControllableTestSubAgent(t, a, taskID)
	sub.setState(SubAgentStateWaitingMain, "need answer")
	a.noteSubAgentStateTransition(sub, SubAgentStateWaitingMain)
	sub.runtimeState.stateChangedAt = time.Now().Add(-time.Minute)

	reason := waitingMainExpiryClosedReasonPrefix + " (no reply within the wait limit)"
	if !a.settleLiveWaitingMainExpiry(sub, reason) {
		t.Fatal("live expiry backed off while the worker was still waiting")
	}
	if got := sub.State(); got != SubAgentStateCancelled {
		t.Fatalf("sub.State() = %q, want cancelled after committed expiry", got)
	}
	if rec := a.taskRecordByTaskID(taskID); rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("record after committed expiry = %#v, want cancelled", rec)
	}
}
