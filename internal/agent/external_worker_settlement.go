package agent

import (
	"time"

	"github.com/keakon/golog/log"
)

// persistExternalTerminalSettlement preserves an autonomous result even when
// shutdown has stopped dispatching events. The journal cannot be overwritten by
// another task's registry write. Memory publication and slot release still
// belong to the close handler, which reuses this exact settlement.
func (w *externalWorker) persistExternalTerminalSettlement(completion *CompletionEnvelope) {
	if w == nil || w.parent == nil {
		return
	}
	w.parent.settlementJournalMu.Lock()
	defer w.parent.settlementJournalMu.Unlock()
	w.promptMu.Lock()
	defer w.promptMu.Unlock()
	// An unproven teardown can still downgrade a result to failed. It must not
	// acquire the immutable settlement's existing-wins protection yet.
	if !w.processEnded {
		return
	}
	owner, record, err := w.cloneExternalTaskRecordLocked()
	if err != nil {
		log.Debugf("skip external terminal settlement task=%v instance=%v reason=%v", w.taskID, w.instanceID, err)
		return
	}
	settlement := &TaskSettlement{
		TaskID: w.taskID, Attempt: max(record.Attempt, 1),
		TerminalRevision: record.LifecycleRevision + 1,
		Outcome:          string(w.State()), Summary: w.LastSummary(),
		Completion: normalizeCompletionEnvelope(completion), SettledAt: time.Now(),
	}
	if settlement.Completion != nil {
		settlement.ArtifactRefs = settlement.Completion.Artifacts
		settlement.ResultRef = settlement.Completion.ResultRef
	}
	// Retain the exact bytes even on a write error: a complete line may have
	// reached disk before fsync failed, and retry must not conflict with it.
	w.terminalSettlement = cloneTaskSettlement(settlement)
	if err := appendTaskSettlement(owner.dir, settlement); err != nil {
		w.persistence.markDegraded(err)
		log.Warnf("persist external terminal settlement task=%v instance=%v error=%v", w.taskID, w.instanceID, err)
		return
	}
	w.terminalSettlementDurable = true
}
