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

func (w *externalWorker) persistExternalSessionLocked() error {
	owner, record, err := w.cloneExternalTaskRecordLocked()
	if err != nil {
		return err
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
	if err := w.parent.persistTaskRegistryRecord(owner.dir, w.taskID, record); err != nil {
		w.persistence.markDegraded(err)
		return fmt.Errorf("persist external input state: %w", err)
	}
	w.parent.subs.mu.Lock()
	w.parent.subs.taskRecords[w.taskID] = record
	w.parent.subs.mu.Unlock()
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

func (w *externalWorker) finishExternalInput() error {
	w.promptMu.Lock()
	defer w.promptMu.Unlock()
	for i := range w.inputs {
		if w.inputs[i].State == message.ToolRecoveryStateOutcomeUnknown {
			w.inputs[i].State = externalInputCompleted
			break
		}
	}
	return w.persistExternalSessionLocked()
}
