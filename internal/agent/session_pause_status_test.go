package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/recovery"
)

func startPauseStatusTestAgent(t *testing.T) *MainAgent {
	t.Helper()
	a := newReadyTestMainAgent(t)
	lock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	a.SetSessionLock(lock)
	startMainAgentLoopForTest(t, a)
	return a
}

func TestSessionPauseRejectedBeforePreparationReturnsPreflight(t *testing.T) {
	t.Parallel()
	a := startPauseStatusTestAgent(t)
	a.admissionPaused.Store(true)
	status, err := a.BeginSessionPause(t.Context(), SessionPauseRequest{Identity: testPauseIdentity(a)})
	if err == nil || status.State != recovery.SessionPausePreflight || status.TransferIdentity != testPauseIdentity(a) {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if a.sessionExecutionPaused() || a.CurrentSessionPauseStatus().TransferID != "" {
		t.Fatal("a rejected preflight changed the source admission or transfer")
	}
	a.admissionPaused.Store(false)
}

func TestSessionPausePreparationFailureConfirmsUnfrozenSource(t *testing.T) {
	t.Parallel()
	a := startPauseStatusTestAgent(t)
	a.SetSessionPausePreparer(func(_ context.Context, _ SessionPauseRequest) error {
		return errors.New("preparation unavailable")
	})
	status, err := a.BeginSessionPause(t.Context(), SessionPauseRequest{Identity: testPauseIdentity(a)})
	if err == nil || status.State != recovery.SessionPausePrepareFailed || status.ErrorCode != "target_not_ready" || status.TransferIdentity != testPauseIdentity(a) {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if a.sessionExecutionPaused() {
		t.Fatal("preparation failure froze source execution")
	}
	a.pause.inputMu.Lock()
	closed := a.pause.inputClosed
	a.pause.inputMu.Unlock()
	if closed {
		t.Fatal("preparation failure did not reopen input admission")
	}
}

func TestSessionPausePublishesBlockerChanges(t *testing.T) {
	t.Parallel()
	a := startPauseStatusTestAgent(t)
	gate := a.sessionExecutionGate()
	first, err := gate.TryBegin(executiongate.Tool, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Finish()
	second, err := gate.TryBegin(executiongate.Tool, "task-2")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Finish()
	status, err := a.BeginSessionPause(t.Context(), SessionPauseRequest{Identity: testPauseIdentity(a)})
	if err != nil || status.BlockerCount != 2 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	first.Finish()
	a.sendEvent(Event{Type: eventSessionPauseWake})
	collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		for _, event := range events {
			if update, ok := event.(SessionTransferEvent); ok && update.Status.State == recovery.SessionPauseDraining && update.Status.BlockerCount == 1 {
				if len(update.Status.Blockers) != 1 || update.Status.Blockers[0].OwnerTaskID != "task-2" {
					t.Fatalf("blockers=%+v", update.Status.Blockers)
				}
				return true
			}
		}
		return false
	})
	second.Finish()
	a.sendEvent(Event{Type: eventSessionPauseWake})
	waitPauseState(t, a, recovery.SessionPauseSaved)
}
