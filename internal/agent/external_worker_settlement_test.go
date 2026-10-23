package agent

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

func TestExternalTerminalCompletionWithoutEventLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"Fixture done."}'
`)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{Description: "Fixture task", AgentType: "external-worker"})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(handle.AgentID).(*externalWorker)
	select {
	case <-w.done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not finish")
	}
	records, err := loadDurableTaskRecords(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	settlements, err := loadTaskSettlements(a.SessionDir())
	if err != nil {
		t.Fatal(err)
	}
	repairTaskRecordsFromSettlements(records, settlements)
	a.setTaskRecords(records)
	a.resetTaskCoordination(a.currentRecoveryOwnership().epoch, settlements)
	a.normalizeRestoredExternalTaskRecords()
	record := a.taskRecordByTaskID(handle.TaskID)
	if record.State != string(SubAgentStateCompleted) || record.LastCompletion == nil ||
		record.LastCompletion.Summary != "Fixture done." || !record.SettlementDurable {
		t.Fatalf("completion lost without event dispatch: %+v", record)
	}
}

func TestExternalTerminalSettlementSurvivesOtherTaskWrite(t *testing.T) {
	for _, state := range []SubAgentState{SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			configureExternalTestRuntime(t, a, "claude", `"*": allow`)
			w := newTestExternalRuntime(t, a, "task-1")
			w.agentDef = a.agentConfigs["external-worker"]
			w.backend = *w.agentDef.Backend
			w.sessionEpoch = a.currentRecoveryOwnership().epoch
			// This fixture never launched a process; its cleanup is proven.
			w.processEnded = true
			switch state {
			case SubAgentStateCompleted:
				w.finishSuccess("Fixture done.")
			case SubAgentStateFailed:
				w.finishFailure(fmt.Errorf("fixture failure"))
			case SubAgentStateCancelled:
				w.finishCancelled("external agent cancelled")
			}
			if err := a.persistTaskRegistryRecord(a.SessionDir(), "task-2", &DurableTaskRecord{
				TaskID: "task-2", LatestInstanceID: "external-2", State: string(SubAgentStateRunning),
			}); err != nil {
				t.Fatal(err)
			}
			records, err := loadDurableTaskRecords(a.SessionDir())
			if err != nil {
				t.Fatal(err)
			}
			settlements, err := loadTaskSettlements(a.SessionDir())
			if err != nil {
				t.Fatal(err)
			}
			repairTaskRecordsFromSettlements(records, settlements)
			if records[w.taskID].State != string(state) {
				t.Fatalf("other task write reverted %s to %s", state, records[w.taskID].State)
			}
			a.closeExternalWorker(w, SubAgentStateCancelled, "late cancel", "late cancel", nil, 0, false)
			after, err := loadTaskSettlements(a.SessionDir())
			if err != nil || len(after) != 1 {
				t.Fatalf("close appended a conflicting settlement: %+v %v", after, err)
			}
			if record := a.taskRecordByTaskID(w.taskID); record.State != string(state) {
				t.Fatalf("close changed autonomous winner: %+v", record)
			}
		})
	}
}

func TestExternalUnprovenCleanupDoesNotJournalCompletion(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w := newTestExternalRuntime(t, a, "task-1")
	w.agentDef = a.agentConfigs["external-worker"]
	w.backend = *w.agentDef.Backend
	w.ctx = context.Background()
	a.subs.mu.Lock()
	a.subs.taskRecords[w.taskID].ExternalSession = &ExternalTaskSession{Backend: config.BackendKindClaudeCode}
	a.subs.mu.Unlock()
	w.sessionEpoch = a.currentRecoveryOwnership().epoch
	w.started.Store(true)
	w.done = make(chan struct{})
	close(w.done)
	w.finishSuccess("Fixture done.")
	settlements, err := loadTaskSettlements(a.SessionDir())
	if err != nil || len(settlements) != 0 {
		t.Fatalf("unproven cleanup acquired a completion winner: %+v %v", settlements, err)
	}
	a.closeExternalWorker(w, SubAgentStateCompleted, "Fixture done.", "", w.externalCompletionEnvelope("Fixture done."), externalTerminationRetryAttempts, false)
	if record := a.taskRecordByTaskID(w.taskID); record.State != string(SubAgentStateFailed) {
		t.Fatalf("unproven completion was not downgraded: %+v", record)
	}
}

func TestExternalTerminalJournalFailureRetriesSameSettlement(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w := newTestExternalRuntime(t, a, "task-1")
	w.agentDef = a.agentConfigs["external-worker"]
	w.backend = *w.agentDef.Backend
	w.sessionEpoch = a.currentRecoveryOwnership().epoch
	w.processEnded = true
	path := taskSettlementJournalPath(a.SessionDir())
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	w.finishSuccess("Fixture done.")
	pending := cloneTaskSettlement(w.terminalSettlement)
	if pending == nil || w.terminalSettlementDurable {
		t.Fatal("journal failure did not retain the exact undurable result")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// Model the other ambiguous write failure: a complete line landed but its
	// sync reported an error. Both retries must use byte-identical content.
	if err := appendTaskSettlement(a.SessionDir(), pending); err != nil {
		t.Fatal(err)
	}
	a.closeExternalWorker(w, SubAgentStateCompleted, "Fixture done.", "", pending.Completion, 0, false)
	settlements, err := loadTaskSettlements(a.SessionDir())
	if err != nil || len(settlements) != 1 {
		t.Fatalf("retry produced conflicting journal entries: %+v %v", settlements, err)
	}
	record := a.taskRecordByTaskID(w.taskID)
	if !record.SettlementDurable || !taskSettlementContentEqual(record.LatestSettlement, pending) {
		t.Fatalf("retry did not recover the original settlement: %+v", record)
	}
}
