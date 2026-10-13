package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keakon/chord/internal/executiongate"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func createPausedTakeoverTarget(t *testing.T, a *MainAgent, sid string) (string, recovery.TransferIdentity) {
	t.Helper()
	sessionsDir, err := a.projectSessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionsDir, sid)
	id := recovery.TransferIdentity{SessionID: sid, TransferID: "transfer-sample", SourceRuntimeID: "source-runtime", SourceSessionEpoch: 1, Workspace: a.ContentRoot(), TargetKind: "tui", TargetInstanceID: "target-runtime"}
	manager := recovery.NewRecoveryManager(path)
	if err := manager.SaveSnapshot(&recovery.SessionSnapshot{Pause: &recovery.SessionPauseCheckpoint{Identity: id, Paused: true, CheckpointComplete: true, CheckpointID: "checkpoint-sample", PendingInputs: []recovery.PendingInput{{InputID: "input-sample", OwnerTaskID: identity.MainAgentID, Content: "pending instruction", FromUser: true, Stage: recovery.PendingInputStageQueued}}}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	return path, id
}

func installTakeoverTestLock(t *testing.T, a *MainAgent) *recovery.SessionLock {
	t.Helper()
	lock, err := recovery.AcquireSessionLock(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetSessionLock(lock)
	return lock
}

func TestSessionTakeoverClosedOwnerRejectsInsteadOfReturningSuccess(t *testing.T) {
	a := newReadyTestMainAgent(t)
	a.signalStopping()
	if err := a.ResumePausedSession(context.Background(), "target", recovery.TransferIdentity{}); !errors.Is(err, ErrAgentShutdown) {
		t.Fatalf("error=%v", err)
	}
}

func TestSessionTakeoverPublicActivationRestoresEmptyPausedCheckpoint(t *testing.T) {
	a, provider := scenarioAgent(t)
	installTakeoverTestLock(t, a)
	path, id := createPausedTakeoverTarget(t, a, "paused-target")
	oldGate := a.sessionExecutionGate()
	startMainAgentLoopForTest(t, a)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := a.ResumePausedSession(ctx, id.SessionID, id); err != nil {
		t.Fatal(err)
	}
	if a.SessionDir() != path || !a.sessionExecutionPaused() || !oldGate.Snapshot().Paused {
		t.Fatal("target was not paused or old gate remained open")
	}
	if len(a.PendingSessionInputs()) != 1 {
		t.Fatal("zero-message checkpoint lost accepted input")
	}
	if calls, _ := provider.snapshot(); len(calls) != 0 {
		t.Fatal("restoring empty checkpoint started a model")
	}
}

func TestSessionTakeoverRestoredEventPublishesCompletedState(t *testing.T) {
	a, _ := scenarioAgent(t)
	installTakeoverTestLock(t, a)
	_, id := createPausedTakeoverTarget(t, a, "paused-target")
	// An unbuffered reply keeps the handler inside its success path until the
	// restored state has been checked, before any deferred cleanup can run.
	reply := make(chan error)
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	}()
	t.Cleanup(func() {
		select {
		case <-reply:
		case <-done:
		}
		<-done
	})
	collectAgentEventsUntil(t, a.Events(), func(events []AgentEvent) bool {
		for _, event := range events {
			if _, ok := event.(SessionRestoredEvent); ok {
				return true
			}
		}
		return false
	})
	if a.admissionPaused.Load() {
		t.Error("restored session retained switching admission state")
	}
}

func TestSessionTakeoverAllowsIdleAndCompletedWorkers(t *testing.T) {
	for _, state := range []SubAgentState{SubAgentStateIdle, SubAgentStateCompleted} {
		t.Run(string(state), func(t *testing.T) {
			a := newReadyTestMainAgent(t)
			installTakeoverTestLock(t, a)
			path, id := createPausedTakeoverTarget(t, a, "paused-target")
			sub := newControllableTestSubAgent(t, a, "worker-sample")
			sub.setState(state, "settled sample")
			reply := make(chan error, 1)
			a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
			if err := <-reply; err != nil {
				t.Fatal(err)
			}
			if a.SessionDir() != path || !a.sessionExecutionPaused() {
				t.Fatal("quiescent worker blocked takeover")
			}
		})
	}
}

func TestSessionTakeoverCurrentWriteFailureKeepsOldOwnership(t *testing.T) {
	a := newReadyTestMainAgent(t)
	oldLock := installTakeoverTestLock(t, a)
	oldPath, oldManager, oldGate := a.SessionDir(), a.recoveryManager(), a.sessionExecutionGate()
	path, id := createPausedTakeoverTarget(t, a, "paused-target")
	cause := errors.New("transcript append unavailable")
	a.persist.rememberFailure(persistEntry{recovery: oldManager, agentID: identity.MainAgentID}, cause)
	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	if err := <-reply; !errors.Is(err, cause) {
		t.Fatalf("error=%v", err)
	}
	if a.SessionDir() != oldPath || a.sessionLock != oldLock || a.recoveryManager() != oldManager || oldGate.Snapshot().Paused {
		t.Fatal("failed save replaced or disabled current session")
	}
	if _, err := recovery.AcquireSessionLock(oldPath); err == nil {
		t.Fatal("failed save released current ownership")
	}
	newLock, err := recovery.AcquireSessionLock(path)
	if err != nil {
		t.Fatal("failed save retained target ownership")
	}
	_ = newLock.Release()
}

func TestSessionTakeoverCurrentSyncFailureKeepsOldSessionRecoverable(t *testing.T) {
	a := newReadyTestMainAgent(t)
	oldLock := installTakeoverTestLock(t, a)
	oldPath, oldManager := a.SessionDir(), a.recoveryManager()
	_, id := createPausedTakeoverTarget(t, a, "paused-target")
	if err := os.Symlink(t.TempDir(), filepath.Join(oldPath, "unavailable-entry")); err != nil {
		t.Fatal(err)
	}
	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	if err := <-reply; err == nil {
		t.Fatal("sync failure was ignored")
	}
	if a.SessionDir() != oldPath || a.sessionLock != oldLock || a.recoveryManager() != oldManager {
		t.Fatal("sync failure activated target")
	}
	if err := os.Remove(filepath.Join(oldPath, "unavailable-entry")); err != nil {
		t.Fatal(err)
	}
	if err := oldManager.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "saved after retry"}); err != nil {
		t.Fatal("old writer was closed despite failed sync")
	}
}

func TestSessionTakeoverClosedCurrentWriterRetainsFrozenOwnership(t *testing.T) {
	a := newReadyTestMainAgent(t)
	oldLock := installTakeoverTestLock(t, a)
	oldPath, oldManager, gate := a.SessionDir(), a.recoveryManager(), a.sessionExecutionGate()
	path, id := createPausedTakeoverTarget(t, a, "paused-target")
	if err := oldManager.Close(); err != nil {
		t.Fatal(err)
	}
	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	if err := <-reply; !errors.Is(err, recovery.ErrClosed) {
		t.Fatalf("error=%v", err)
	}
	a.pause.inputMu.Lock()
	inputClosed := a.pause.inputClosed
	a.pause.inputMu.Unlock()
	if a.SessionDir() != oldPath || a.sessionLock != oldLock || !gate.Snapshot().Paused || !inputClosed {
		t.Fatal("closed writer failure replaced or reopened unsafe current session")
	}
	if _, err := recovery.AcquireSessionLock(oldPath); err == nil {
		t.Fatal("closed writer failure released current ownership")
	}
	lock, err := recovery.AcquireSessionLock(path)
	if err != nil {
		t.Fatal("closed writer failure retained target lock")
	}
	_ = lock.Release()
}

func TestSessionTakeoverRejectsBackgroundWorkWithoutStoppingIt(t *testing.T) {
	restore := tools.ResetJobRegistryForTest()
	defer restore()
	a := newReadyTestMainAgent(t)
	oldLock := installTakeoverTestLock(t, a)
	oldPath := a.SessionDir()
	_, id := createPausedTakeoverTarget(t, a, "paused-target")
	jobID, err := tools.ExecuteJobForTest(context.Background(), "sleep 60", "sample background task", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tools.StopJobByUser(jobID, "test cleanup") })
	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	if err := <-reply; err == nil {
		t.Fatal("background work did not block takeover")
	}
	peek, ok := tools.PeekJobForDisplay(jobID, 16)
	if !ok || peek.Status != "running" || a.SessionDir() != oldPath || a.sessionLock != oldLock {
		t.Fatal("takeover stopped active background work")
	}
}

func TestSessionTakeoverFreezesQueuedAuxiliaryAdmissionBeforeWaiting(t *testing.T) {
	a := newReadyTestMainAgent(t)
	installTakeoverTestLock(t, a)
	_, id := createPausedTakeoverTarget(t, a, "paused-target")
	gate := a.sessionExecutionGate()
	result := make(chan error, 1)
	a.outputWg.Go(func() {
		<-gate.Frozen()
		lease, err := gate.TryBegin(executiongate.LLM, "translation")
		if lease != nil {
			lease.Finish()
		}
		result <- err
	})
	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	if err := <-reply; err != nil {
		t.Fatal(err)
	}
	if err := <-result; !executiongate.IsPaused(err) {
		t.Fatalf("queued auxiliary error=%v", err)
	}
}

func TestSessionTakeoverInFlightAuxiliaryKeepsAdmissionAndOwnership(t *testing.T) {
	a := newReadyTestMainAgent(t)
	oldLock := installTakeoverTestLock(t, a)
	oldPath, oldManager, gate := a.SessionDir(), a.recoveryManager(), a.sessionExecutionGate()
	path, id := createPausedTakeoverTarget(t, a, "paused-target")
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	leaseErr := make(chan error, 1)
	go func() {
		lease, err := gate.TryBegin(executiongate.LLM, "auxiliary")
		leaseErr <- err
		close(started)
		<-release
		lease.Finish()
		close(finished)
	}()
	<-started
	t.Cleanup(func() { close(release); <-finished })
	if err := <-leaseErr; err != nil {
		t.Fatal(err)
	}
	reply := make(chan error, 1)
	a.handleTakeoverTarget(sessionTakeoverTarget{ctx: t.Context(), sessionID: id.SessionID, identity: id, reply: reply})
	if err := <-reply; err == nil {
		t.Fatal("active auxiliary request did not reject takeover")
	}
	a.pause.inputMu.Lock()
	inputClosed := a.pause.inputClosed
	a.pause.inputMu.Unlock()
	if a.SessionDir() != oldPath || a.sessionLock != oldLock || a.recoveryManager() != oldManager || gate.Snapshot().Paused || inputClosed {
		t.Fatal("target preflight failure left old admission or ownership changed")
	}
	lease, err := gate.TryBegin(executiongate.Tool, "sample")
	if err != nil {
		t.Fatal("rejected takeover froze old admission")
	}
	lease.Finish()
	if err := oldManager.PersistMessage(identity.MainAgentID, message.Message{Role: message.RoleUser, Content: "continue current session"}); err != nil {
		t.Fatal("rejected takeover closed old writer")
	}
	lock, err := recovery.AcquireSessionLock(path)
	if err != nil {
		t.Fatal("rejected takeover retained target lock")
	}
	_ = lock.Release()
}

func TestOrdinaryResumeAcceptsOnlyCompletePausedEmptyTranscript(t *testing.T) {
	for _, paused := range []bool{true, false} {
		t.Run(map[bool]string{true: "paused", false: "pending_before_dispatch"}[paused], func(t *testing.T) {
			for _, complete := range []bool{true, false} {
				t.Run(map[bool]string{true: "complete", false: "incomplete"}[complete], func(t *testing.T) {
					a, provider := scenarioAgent(t)
					path, id := createPausedTakeoverTarget(t, a, "paused-target")
					manager := recovery.NewRecoveryManager(path)
					snapshot, err := manager.Recover()
					if err != nil {
						t.Fatal(err)
					}
					snapshot.Pause.CheckpointComplete = complete
					snapshot.Pause.Paused = paused
					if err := manager.SaveSnapshot(snapshot); err != nil {
						t.Fatal(err)
					}
					_ = manager.Close()
					resolved, err := a.resolveResumeSessionPath(id.SessionID)
					if !complete {
						if err == nil {
							t.Fatal("incomplete empty checkpoint accepted")
						}
						if _, err := a.loadSessionState(path); err == nil {
							t.Fatal("incomplete empty checkpoint loaded")
						}
						return
					}
					if err != nil || resolved != path {
						t.Fatalf("resolved=%s, error=%v", resolved, err)
					}
					startMainAgentLoopForTest(t, a)
					a.ResumeSessionID(id.SessionID)
					deadline := time.NewTimer(5 * time.Second)
					defer deadline.Stop()
					for {
						select {
						case event := <-a.Events():
							if _, ok := event.(SessionRestoredEvent); ok {
								if !a.sessionExecutionPaused() || len(a.PendingSessionInputs()) != 1 {
									t.Fatal("ordinary resume did not preserve paused inputs")
								}
								if calls, _ := provider.snapshot(); len(calls) != 0 {
									t.Fatal("ordinary paused resume started model")
								}
								return
							}
						case <-deadline.C:
							t.Fatal("ordinary empty paused resume did not complete")
						}
					}
				})
			}
		})
	}
}

func TestOrdinaryResumeRejectsEmptyUnpausedCheckpointWithoutPendingInput(t *testing.T) {
	a := newReadyTestMainAgent(t)
	path, id := createPausedTakeoverTarget(t, a, "paused-target")
	manager := recovery.NewRecoveryManager(path)
	snapshot, err := manager.Recover()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Pause.Paused = false
	snapshot.Pause.PendingInputs = nil
	if err := manager.SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.resolveResumeSessionPath(id.SessionID); err == nil {
		t.Fatal("empty running checkpoint accepted without pending input")
	}
	if _, err := a.loadSessionState(path); err == nil {
		t.Fatal("empty running checkpoint loaded without pending input")
	}
}
