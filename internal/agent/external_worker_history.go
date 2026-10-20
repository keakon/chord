package agent

import (
	"fmt"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func (w *externalWorker) persistExternalMessage(msg message.Message) error {
	manager := w.parent.recoveryManagerForEpoch(w.sessionEpoch)
	if manager == nil {
		return fmt.Errorf("external worker requires durable session storage")
	}
	w.historyMu.Lock()
	defer w.historyMu.Unlock()
	if err := manager.PersistMessageDurable(w.instanceID, msg); err != nil {
		w.persistence.markDegraded(err)
		return fmt.Errorf("persist external worker message: %w", err)
	}
	w.ctxMgr.Append(msg.Clone())
	return nil
}

func (w *externalWorker) messageSnapshot() []message.Message {
	if w.ctxMgr == nil {
		return nil
	}
	return w.ctxMgr.Snapshot()
}

func (w *externalWorker) persistExternalToolStarted(id, name string, audit *message.ToolArgsAudit) error {
	manager := w.parent.recoveryManagerForEpoch(w.sessionEpoch)
	if manager == nil {
		return fmt.Errorf("external tool authorization requires durable session storage")
	}
	err := manager.AppendToolActivity(recovery.ToolActivityRecord{
		AgentID: w.instanceID, CallID: id, Tool: name, State: recovery.ToolActivityStateStarted,
		WorkDir: w.workDir, WorkDirGeneration: w.workDirState.Generation,
		Audit: audit, TS: time.Now().UnixNano(),
	})
	if err != nil {
		w.persistence.markDegraded(err)
		return fmt.Errorf("persist external tool authorization: %w", err)
	}
	return nil
}

func (w *externalWorker) settleUnresolvedExternalTools() error {
	w.toolMu.Lock()
	names := make(map[string]string, len(w.toolNames))
	for id, name := range w.toolNames {
		names[id] = name
	}
	w.toolMu.Unlock()
	for id, name := range names {
		state := message.ToolRecoveryStateNotStarted
		if binding := w.toolBindings[id]; binding != nil && binding.started {
			state = message.ToolRecoveryStateOutcomeUnknown
		}
		w.settleExternalTool(id, name, "External execution did not produce a recorded result; do not replay automatically.", message.ToolStatusError, state)
	}
	if len(names) != 0 {
		return fmt.Errorf("external agent left %d tool results unknown", len(names))
	}
	return nil
}

func (w *externalWorker) settleExternalTool(id, name, text, status, recoveryState string) {
	if binding := w.toolBindings[id]; binding != nil && binding.settled {
		return
	}
	diff := w.buildToolDiff(id)
	msg := message.Message{
		Role: message.RoleTool, ToolCallID: id, Content: text, ToolStatus: status,
		ToolRecoveryState: recoveryState, ToolDiff: diff.Text,
		ToolDiffAdded: diff.Added, ToolDiffRemoved: diff.Removed,
	}
	if err := w.persistExternalMessage(msg); err != nil {
		w.setResultError(err)
		status = message.ToolStatusError
		text = "External tool result could not be persisted; execution outcome is unknown."
	}
	w.parent.emitToTUI(ToolResultEvent{
		CallID: id, Name: name, Result: text, Payload: text,
		Status: message.ToolResultStatus(status), AgentID: w.instanceID,
		Diff: diff.Text, DiffAdded: diff.Added, DiffRemoved: diff.Removed,
	})
	if binding := w.toolBindings[id]; binding != nil {
		binding.settled = true
	}
	w.forgetToolName(id)
}

func (w *externalWorker) setResultError(err error) {
	w.resultMu.Lock()
	if err == nil {
		w.resultError = nil
	} else if w.resultError == nil {
		w.resultError = err
	}
	w.resultMu.Unlock()
}

func (w *externalWorker) getResultError() error {
	w.resultMu.Lock()
	defer w.resultMu.Unlock()
	return w.resultError
}
