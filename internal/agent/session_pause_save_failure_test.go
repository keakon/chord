package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func lockPauseTestSession(t *testing.T, a *MainAgent) {
	t.Helper()
	lock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetSessionLock(lock)
	t.Cleanup(func() { _ = lock.Release() })
}

func assertFailedPauseKeepsOwnership(t *testing.T, a *MainAgent) {
	t.Helper()
	if !a.sessionExecutionPaused() {
		t.Fatal("save failure reopened execution")
	}
	if lock, err := recovery.AcquireSessionLock(a.SessionDir()); err == nil {
		_ = lock.Release()
		t.Fatal("save failure released source ownership")
	}
	if err := a.ReleasePausedSession("transfer-1"); err == nil {
		t.Fatal("failed save could release session")
	}
}

func TestSessionPauseSnapshotDirectoryFailureCanRetryThroughMainLoop(t *testing.T) {
	t.Parallel()
	for _, initial := range []bool{true, false} {
		name := "final_checkpoint"
		if initial {
			name = "initial_checkpoint"
		}
		t.Run(name, func(t *testing.T) {
			a, provider := scenarioAgent(t)
			lockPauseTestSession(t, a)
			lease, err := a.sessionExecutionGate().TryBegin(executiongate.Tool, "main")
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Finish()
			a.newTurn("")
			if !submitMainDraftForTest(a, "draft-1", []message.ContentPart{{Type: message.ContentPartText, Text: "Queued constraint"}}) {
				t.Fatal("draft rejected")
			}
			path := filepath.Join(a.SessionDir(), "snapshot.json")
			if initial {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			startMainAgentLoopForTest(t, a)
			status, err := a.BeginSessionPause(context.Background(), SessionPauseRequest{Identity: testPauseIdentity(a)})
			if initial {
				if err == nil || status.State != recovery.SessionPauseSaveFailed {
					t.Fatalf("status=%+v err=%v", status, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				lease.Finish()
				a.sendEvent(Event{Type: eventSessionPauseWake})
				status = waitPauseState(t, a, recovery.SessionPauseSaveFailed)
			}
			assertFailedPauseKeepsOwnership(t, a)
			if status.RecoverableAction != "retry-save" {
				t.Fatalf("repair action=%q", status.RecoverableAction)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if !initial {
				if err := os.Remove(path + ".saved"); err != nil {
					t.Fatal(err)
				}
			}
			var retries sync.WaitGroup
			results := make(chan error, 4)
			for range cap(results) {
				retries.Go(func() { results <- a.RetrySessionPauseSave(context.Background(), "transfer-1") })
			}
			retries.Wait()
			close(results)
			accepted := 0
			for err := range results {
				if err == nil {
					accepted++
					continue
				}
				pauseErr, ok := errors.AsType[*recovery.SessionPauseError](err)
				if !ok || (pauseErr.Code != "transfer_busy" && pauseErr.Code != "invalid_request") {
					t.Fatalf("unexpected retry result: %v", err)
				}
			}
			if accepted != 1 {
				t.Fatalf("accepted retries=%d, want 1", accepted)
			}
			lease.Finish()
			a.sendEvent(Event{Type: eventSessionPauseWake})
			waitPauseState(t, a, recovery.SessionPauseSaved)
			if err := a.ReleasePausedSession("transfer-1"); err != nil {
				t.Fatal(err)
			}
			manager := recovery.NewRecoveryManager(a.SessionDir())
			defer manager.Close()
			snapshot, err := manager.Recover()
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Pause == nil || !snapshot.Pause.CheckpointComplete || len(snapshot.Pause.PendingInputs) != 0 || snapshot.Pause.InputDeliveryRevision == 0 {
				t.Fatalf("recovered checkpoint=%+v", snapshot.Pause)
			}
			queue, err := manager.LoadInputDelivery()
			if err != nil || queue == nil || len(queue.Inputs) != 1 || queue.Inputs[0].Content != "Queued constraint" {
				t.Fatalf("queue=%+v error=%v", queue, err)
			}
			if requests, _ := provider.snapshot(); len(requests) != 0 {
				t.Fatal("save retry dispatched model work")
			}
		})
	}
}

func TestSessionPauseStickyCloseFailureStaysFrozenThroughMainLoop(t *testing.T) {
	t.Parallel()
	a, provider := scenarioAgent(t)
	lockPauseTestSession(t, a)
	ledger := analytics.NewUsageLedger(a.SessionDir(), a.ContentRoot())
	a.usageLedger = ledger
	path := filepath.Join(a.SessionDir(), analytics.SessionUsageSummaryFileName)
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	closeErr := ledger.Close()
	if closeErr == nil {
		t.Fatal("directory fault did not fail usage close")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	startMainAgentLoopForTest(t, a)
	if _, err := a.BeginSessionPause(context.Background(), SessionPauseRequest{Identity: testPauseIdentity(a)}); err != nil {
		t.Fatal(err)
	}
	status := waitPauseState(t, a, recovery.SessionPauseSaveFailed)
	if status.RecoverableAction != "manual-recovery" {
		t.Fatalf("action=%q error=%q", status.RecoverableAction, status.Error)
	}
	assertFailedPauseKeepsOwnership(t, a)
	err := a.RetrySessionPauseSave(context.Background(), "transfer-1")
	if _, ok := errors.AsType[*SessionPauseWriterClosedError](err); !ok || !errors.Is(err, closeErr) {
		t.Fatalf("sticky close error lost: %v", err)
	}
	status, _ = a.SessionPauseStatus("transfer-1")
	if status.State != recovery.SessionPauseSaveFailed || status.RecoverableAction != "manual-recovery" {
		t.Fatalf("unsafe retry status=%+v", status)
	}
	assertFailedPauseKeepsOwnership(t, a)
	select {
	case <-a.persist.done:
	default:
		t.Fatal("failure did not exercise already closed pump")
	}
	if requests, _ := provider.snapshot(); len(requests) != 0 {
		t.Fatal("close retry dispatched model work")
	}
}

func TestSessionPauseRetryRejectsConcurrentAdmissionAndStaleCompletion(t *testing.T) {
	t.Parallel()
	a := newReadyTestMainAgent(t)
	id := testPauseIdentity(a)
	active := &sessionPauseCommand{req: SessionPauseRequest{Identity: id}, result: make(chan error, 1)}
	a.pause.retry = active
	a.pause.status = recovery.SessionPauseStatus{TransferIdentity: id, State: recovery.SessionPauseSaveFailed}
	other := &sessionPauseCommand{req: active.req, result: make(chan error, 1)}
	a.dispatchSessionPause(Event{Type: eventSessionPauseRetry, Payload: other})
	if err := <-other.result; err == nil {
		t.Fatal("concurrent retry was admitted")
	} else if pauseErr, ok := errors.AsType[*recovery.SessionPauseError](err); !ok || pauseErr.Code != "transfer_busy" {
		t.Fatalf("concurrent retry error=%v", err)
	}
	a.dispatchSessionPause(Event{Type: eventSessionPauseRetried, Payload: sessionPauseResult{command: other, err: errors.New("sample stale failure")}})
	if a.pause.retry != active || a.pause.status.Error != "" || a.pause.status.State != recovery.SessionPauseSaveFailed {
		t.Fatal("stale completion changed active retry")
	}
	select {
	case <-active.result:
		t.Fatal("stale completion acknowledged active retry")
	default:
	}
	a.pause.retry = nil
}
