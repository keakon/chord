package agent

import (
	"fmt"
	"slices"
	"time"

	"github.com/keakon/chord/internal/message"
)

// cloneExternalTaskRecordLocked returns the recovery owner together with a
// private copy of the task's current durable record, or an error when this
// worker no longer owns the durable write. Callers must hold promptMu.
func (w *externalWorker) cloneExternalTaskRecordLocked() (*recoveryOwnership, *DurableTaskRecord, error) {
	owner := w.parent.currentRecoveryOwnership()
	if owner == nil || owner.epoch != w.sessionEpoch {
		return nil, nil, fmt.Errorf("external task storage belongs to a replaced session")
	}
	w.parent.subs.mu.RLock()
	record := cloneDurableTaskRecord(w.parent.subs.taskRecords[w.taskID])
	w.parent.subs.mu.RUnlock()
	if record == nil || record.LatestInstanceID != w.instanceID {
		return nil, nil, fmt.Errorf("external task instance is no longer current")
	}
	return owner, record, nil
}

// persistExternalSessionLocked publishes and persists the worker's durable
// input state. Callers hold promptMu; under it the lock order is
// promptMu -> taskRegistryPersistMu -> subs.mu, the same tail order
// persistTaskRegistryRecord takes, so no path may acquire them in reverse.
//
// The record is published to the registry before it is written: a mailbox that
// arrives during the write can then extend this record without a later stale
// publication replacing its fields. A failed write therefore leaves the
// registry ahead of the file. That is deliberate: the caller marks persistence
// degraded and recovery reads the file, so the task fails loudly instead of the
// registry quietly reporting state no restart would see.
func (w *externalWorker) persistExternalSessionLocked() error {
	a := w.parent
	a.taskRegistryPersistMu.Lock()
	defer a.taskRegistryPersistMu.Unlock()
	owner := a.currentRecoveryOwnership()
	if owner == nil || owner.epoch != w.sessionEpoch {
		return fmt.Errorf("external task storage belongs to a replaced session")
	}
	a.subs.mu.Lock()
	record := cloneDurableTaskRecord(a.subs.taskRecords[w.taskID])
	if record == nil || record.LatestInstanceID != w.instanceID {
		a.subs.mu.Unlock()
		return fmt.Errorf("external task instance is no longer current")
	}
	record.State = string(w.State())
	record.ResumePolicy = durableTaskResumePolicy(w.State())
	record.LastSummary = w.LastSummary()
	record.Persistence = w.PersistenceHealth()
	record.ExternalSession.SessionID = w.currentSessionID()
	record.ExternalSession.SessionEstablished = w.currentSessionEstablished()
	record.ExternalSession.WorkDir = w.workDir
	record.ExternalSession.Inputs = slices.Clone(w.inputs)
	record.ExternalSession.ProcessEnded = w.processEnded
	record.ExternalSession.RunID = w.runID
	if w.baseline != nil {
		baseline := *w.baseline
		record.ExternalSession.Baseline = &baseline
	} else {
		record.ExternalSession.Baseline = nil
	}
	record.UpdatedAt = time.Now()
	// Publish before the snapshot, as other registry updates do. A mailbox
	// arriving during the write can then extend this record without a later
	// stale publication replacing its fields.
	a.subs.taskRecords[w.taskID] = record
	records := cloneDurableTaskRecordMap(a.subs.taskRecords)
	a.subs.mu.Unlock()
	if hook := a.taskRegistryPersistHook; hook != nil {
		hook()
	}
	if err := persistDurableTaskRecords(owner.dir, records); err != nil {
		w.persistence.markDegraded(err)
		return fmt.Errorf("persist external input state: %w", err)
	}
	return nil
}

func (w *externalWorker) sendExternalInput(input externalTaskInput) error {
	w.promptMu.Lock()
	for i := range w.inputs {
		if w.inputs[i].ID == input.ID {
			w.inputs[i].State = message.ToolRecoveryStateOutcomeUnknown
		}
	}
	w.processEnded = false
	err := w.persistExternalSessionLocked()
	w.lastReplyMessageID = ""
	if input.Mailbox != nil {
		w.lastReplyMessageID = input.Mailbox.MessageID
	}
	w.promptMu.Unlock()
	if err != nil {
		return err
	}
	return w.currentSession().WriteUserMessage(w.ctx, input.Content)
}

func (w *externalWorker) finishExternalInputLocked() error {
	for i := range w.inputs {
		if w.inputs[i].State == message.ToolRecoveryStateOutcomeUnknown {
			w.inputs = slices.Delete(w.inputs, i, i+1)
			break
		}
	}
	return w.persistExternalSessionLocked()
}
