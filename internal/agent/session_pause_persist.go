package agent

import (
	"fmt"

	"github.com/keakon/chord/internal/recovery"
)

func (a *MainAgent) saveInitialSessionPause(cmd *sessionPauseCommand, checkpoint *recovery.SessionPauseCheckpoint, snapshot *recovery.SessionSnapshot) {
	err := a.captureSubPauseInputs(checkpoint)
	manager := a.recoveryManager()
	if err == nil && manager == nil {
		err = fmt.Errorf("session pause has no recovery manager")
	}
	if err == nil {
		checkpoint.PendingInputs, err = manager.PersistPendingInputs(checkpoint.PendingInputs)
	}
	if err == nil {
		a.recoverySnapshotMu.Lock()
		a.pause.checkpoint = checkpoint.Clone()
		snapshot.Pause = checkpoint.Clone()
		err = manager.SaveSnapshot(snapshot)
		a.recoverySnapshotMu.Unlock()
	}
	// Preserve accepted inputs independently before checking transcript writes.
	// Capture runs after each owner has queued its staged input writes, so this
	// barrier covers them without waiting for active requests to finish. Restore
	// derives staging from the log if a crash interrupts either persistence step.
	if err == nil {
		err = a.persist.sessionPauseBarrier()
	}
	if err == nil {
		err = a.persist.strictError(manager)
	}
	if err == nil {
		err = syncSessionDurabilityBarrier(manager)
	}
	a.sendEvent(Event{Type: eventSessionPauseCheckpoint, Payload: sessionPauseResult{command: cmd, checkpoint: checkpoint, err: err}})
}

func (a *MainAgent) drainSubPauseInputs(checkpoint *recovery.SessionPauseCheckpoint) error {
	for _, sub := range a.subs.snapshotSubAgents() {
		capture, err := sub.DrainSessionPause(a.parentCtx)
		if err != nil {
			return fmt.Errorf("drain task %s: %w", sub.taskID, err)
		}
		for _, p := range capture.PendingInputs {
			checkpoint.PendingInputs = append(checkpoint.PendingInputs, pendingPauseInput(p, capture.TaskID))
		}
		for _, msg := range capture.ContextAppends {
			p := pendingUserMessage{InputID: msg.InputID, Content: msg.Content, Parts: msg.Parts, Kind: msg.Kind, Mailbox: msg.Mailbox}
			if p.InputID == "" {
				p.InputID = checkpoint.CheckpointID + ":" + capture.TaskID + fmt.Sprint(len(checkpoint.PendingInputs))
			}
			checkpoint.PendingInputs = append(checkpoint.PendingInputs, pendingPauseInput(p, capture.TaskID))
		}
		if err := sub.StopAfterSessionPause(a.parentCtx); err != nil {
			return fmt.Errorf("stop drained task %s: %w", sub.taskID, err)
		}
	}
	return nil
}

func (a *MainAgent) saveFinalSessionPause(checkpoint *recovery.SessionPauseCheckpoint) {
	// This is an owner-loop barrier after every sub producer has stopped and
	// before the immutable snapshot leaves the loop.
	a.pause.saving = true
	checkpoint.CheckpointComplete = true
	a.setSessionPauseStatus(func(s *recovery.SessionPauseStatus) {
		s.State = recovery.SessionPauseSaving
		s.PendingInputCount = len(checkpoint.PendingInputs) + a.managedDeliveryInputCount()
		s.CheckpointID = checkpoint.CheckpointID
	})
	snapshot := a.buildRecoverySnapshot()
	snapshot.Pause = checkpoint.Clone()
	a.pause.finalSnapshot = snapshot
	// The retry snapshot remains immutable on the owner loop. Persistence
	// replaces attachment references only in its worker-owned copy.
	workerSnapshot := *snapshot
	workerSnapshot.Pause = snapshot.Pause.Clone()
	snapshot = &workerSnapshot
	go func() {
		err := a.sealSessionPauseProducers()
		if a.walltime != nil {
			a.walltime.flush()
		}
		if err == nil {
			err = a.flushSessionPausePersistence()
		}
		manager := a.recoveryManager()
		if err == nil && manager == nil {
			err = fmt.Errorf("session pause has no recovery writer")
		}
		if err == nil {
			err = syncSessionDurabilityBarrier(manager)
		}
		if err == nil {
			a.recoverySnapshotMu.Lock()
			var inputs []recovery.PendingInput
			inputs, err = manager.PersistPendingInputs(checkpoint.PendingInputs)
			if err == nil {
				checkpoint.PendingInputs = inputs
				a.pause.checkpoint = checkpoint.Clone()
				snapshot.Pause = checkpoint.Clone()
				err = manager.SaveSnapshot(snapshot)
			}
			a.recoverySnapshotMu.Unlock()
		}
		if err == nil {
			a.shuttingDown.Store(true)
			a.closePersistLoopUntil(nil)
			<-a.persist.done
			err = a.persist.strictError(manager)
			if err == nil {
				err = a.persist.rememberSessionPauseCloseFailure(manager.Close())
			}
			if err == nil && a.usageLedger != nil {
				err = a.persist.rememberSessionPauseCloseFailure(a.usageLedger.Close())
			}
			if err == nil {
				err = a.persist.rememberSessionPauseCloseFailure(a.persist.strictError(manager))
			}
		}
		if err == nil {
			a.clearRecoveryManagerIf(manager)
		}
		a.sendEvent(Event{Type: eventSessionPauseSaved, Payload: sessionPauseResult{checkpoint: checkpoint, err: err}})
	}()
}
