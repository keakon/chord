package agent

import (
	"errors"
	"fmt"

	"github.com/keakon/chord/internal/recovery"
	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) takeoverTargetHasActiveWork() bool {
	for _, sub := range a.subs.snapshotSubAgents() {
		if !sub.canPark() {
			return true
		}
	}
	for _, job := range tools.SnapshotJobs() {
		if job.Active() && (job.SessionDir == "" || job.SessionDir == a.SessionDir()) {
			return true
		}
	}
	return false
}

// freezeTakeoverCurrentSession preserves the current session's ownership until
// every old writer has proved durability. Failed closes retain the frozen
// session; restarting and ordinary resume remain available for recovery.
func (a *MainAgent) freezeTakeoverCurrentSession(finishStructural func()) (err error) {
	manager := a.recoveryManager()
	if manager == nil {
		return pauseError("persistence_failed", "current session recovery writer is unavailable")
	}
	gate := a.sessionExecutionGate()
	// Admission and the activity check share one lock. An auxiliary request
	// that wins the race must keep its current session fully usable.
	if _, frozen := gate.TryFreeze(1); !frozen {
		return pauseError("target_not_ready", "current session still has active execution")
	}
	a.contextDiagnostics.invalidate()
	a.pause.inputMu.Lock()
	wasInputClosed := a.pause.inputClosed
	a.pause.inputClosed = true
	a.pause.inputMu.Unlock()
	closed := false
	defer func() {
		finishStructural()
		if err == nil {
			return
		}
		if closed || errors.Is(err, recovery.ErrClosed) {
			a.notePersistenceFailure(err)
			a.emitToTUI(ToastEvent{Message: "The current session could not close safely. Restart and resume it before continuing", Level: "warn"})
			return
		}
		if a.subPersists != nil {
			a.subPersists.mu.Lock()
			a.subPersists.sealed = false
			a.subPersists.mu.Unlock()
		}
		if resumeErr := gate.Resume(); resumeErr != nil {
			err = errors.Join(err, resumeErr)
			return
		}
		a.pause.inputMu.Lock()
		a.contextDiagnostics.reset()
		a.pause.inputClosed = wasInputClosed
		a.pause.inputMu.Unlock()
	}()
	a.outputWg.Wait()
	a.toolWg.Wait()
	a.compactionWg.Wait()
	for _, sub := range a.subs.snapshotSubAgents() {
		if !a.parkSubAgent(sub.instanceID) {
			return pauseError("persistence_failed", "could not durably park the current session's idle worker")
		}
		if sub.started.Load() {
			<-sub.done
		}
		sub.llmWG.Wait()
	}
	if err = a.sealSessionPauseProducers(); err != nil {
		return fmt.Errorf("flush current session metadata: %w", err)
	}
	if a.walltime != nil {
		a.walltime.flush()
	}
	if err = a.persist.sessionPauseBarrier(); err != nil {
		return fmt.Errorf("flush current session transcript: %w", err)
	}
	if err = a.persist.strictError(manager); err != nil {
		return fmt.Errorf("persist current session transcript: %w", err)
	}
	if a.usageLedger != nil {
		if err = a.usageLedger.Sync(); err != nil {
			return fmt.Errorf("sync current session usage: %w", err)
		}
	}
	if err = a.saveRecoverySnapshotChecked(); err != nil {
		return err
	}
	if err = syncSessionDurabilityBarrier(manager); err != nil {
		return fmt.Errorf("sync current session: %w", err)
	}
	closed = true
	if err = manager.Close(); err != nil {
		return fmt.Errorf("close current session: %w", err)
	}
	if a.usageLedger != nil {
		if err = a.usageLedger.Close(); err != nil {
			return fmt.Errorf("close current session usage: %w", err)
		}
		a.usageLedger = nil
	}
	a.clearRecoveryManagerIf(manager)
	return nil
}
