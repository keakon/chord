package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/externalsupervisor"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestExternalTaskFollowupStartsNewAttempt(t *testing.T) {
	root := t.TempDir()
	launches := filepath.Join(root, "launches.txt")
	inputs := filepath.Join(root, "inputs.txt")
	script := writeFakeClaude(t, strings.NewReplacer("__LAUNCHES__", launches, "__INPUTS__", inputs).Replace(`
printf '%s\n' "$*" >> "__LAUNCHES__"
printf '%s\n' "$prompt" >> "__INPUTS__"
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
`))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	first := driveExternalTask(t, a, handle.TaskID)
	if first.State != string(SubAgentStateCompleted) || !first.ExternalSession.ProcessEnded {
		t.Fatalf("first attempt = %+v", first)
	}
	next, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "Followup input.", string(SubAgentMailboxKindDecisionRequired))
	if err != nil {
		t.Fatal(err)
	}
	second := driveExternalTask(t, a, handle.TaskID)
	if !next.Rehydrated || next.AgentID == handle.AgentID || second.Attempt != 2 || second.State != string(SubAgentStateCompleted) {
		t.Fatalf("followup handle=%+v task=%+v", next, second)
	}
	data, err := os.ReadFile(launches)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "\n") != 2 || !strings.Contains(string(data), "--resume "+first.ExternalSession.SessionID) {
		t.Fatalf("launch arguments = %s", data)
	}
	data, err = os.ReadFile(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "Initial input.") != 1 || strings.Count(string(data), "Followup input.") != 1 {
		t.Fatalf("input replayed = %s", data)
	}
}

func TestExternalTaskFollowupAfterResultErrorStartsNewAttempt(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
case "$*" in
*--resume*)
	printf '%s\n' '{"type":"result","subtype":"success","result":"Recovered."}'
	;;
*)
	printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"boom"}'
	exit 1
	;;
esac
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	first := driveExternalTask(t, a, handle.TaskID)
	if first.State != string(SubAgentStateFailed) {
		t.Fatalf("first attempt = %+v, want failed", first)
	}
	// An error result is a definitive outcome for the input, so a targeted
	// follow-up must not be blocked by an unknown one.
	for _, input := range first.ExternalSession.Inputs {
		if input.State != externalInputCompleted {
			t.Fatalf("input state after an error result = %q, want %q", input.State, externalInputCompleted)
		}
	}
	if _, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "Try again.", string(SubAgentMailboxKindDecisionRequired)); err != nil {
		t.Fatalf("follow-up after an error result: %v", err)
	}
	second := driveExternalTask(t, a, handle.TaskID)
	if second.Attempt != 2 || second.State != string(SubAgentStateCompleted) {
		t.Fatalf("follow-up after an error result = %+v", second)
	}
}

func TestExternalTaskResumeKeepsPersistedCheckoutBaseline(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	first := driveExternalTask(t, a, handle.TaskID)
	if first.State != string(SubAgentStateCompleted) {
		t.Fatalf("first attempt = %+v", first)
	}
	persisted := &externalCheckoutBaseline{Head: "0123456789abcdef0123456789abcdef01234567", Dirty: true}
	a.subs.mu.Lock()
	record := a.subs.taskRecords[handle.TaskID]
	if record == nil || record.ExternalSession == nil {
		a.subs.mu.Unlock()
		t.Fatalf("task record = %+v", record)
	}
	record.ExternalSession.Baseline = persisted
	a.subs.mu.Unlock()
	if _, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "Followup input.", string(SubAgentMailboxKindDecisionRequired)); err != nil {
		t.Fatal(err)
	}
	second := driveExternalTask(t, a, handle.TaskID)
	if second.Attempt != 2 || second.State != string(SubAgentStateCompleted) {
		t.Fatalf("second attempt = %+v", second)
	}
	got := second.ExternalSession.Baseline
	if got == nil || got.Head != persisted.Head || !got.Dirty || got.StatusUnknown {
		t.Fatalf("resume recaptured the checkout baseline: %+v", got)
	}
}

func TestExternalTaskSealedInputQueuedUntilProcessExit(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "release")
	script := writeFakeClaude(t, strings.ReplaceAll(`
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
case "$*" in
  *--resume*) ;;
  *) while [ ! -f "__RELEASE__" ]; do sleep 0.01; done ;;
esac
`, "__RELEASE__", release))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(handle.AgentID).(*externalWorker)
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.promptMu.Lock()
		sealed := w.inputClosed
		w.promptMu.Unlock()
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input did not seal")
		}
		time.Sleep(time.Millisecond)
	}
	next, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "Queued input.", string(SubAgentMailboxKindDecisionRequired))
	if err != nil || next.Status != "queued" {
		t.Fatalf("sealed followup = %+v %v", next, err)
	}
	if len(a.sem) != 1 || a.taskRecordByTaskID(handle.TaskID).Attempt != 1 {
		t.Fatal("slot released or new attempt started before process exit")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) || record.Attempt != 2 {
		t.Fatalf("queued followup = %+v", record)
	}
}

func writeSupervisorReceipt(t *testing.T, dir string, receipt externalsupervisor.Receipt) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, receipt.RunID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExternalTaskResumeWithoutEstablishedSessionStartsFresh(t *testing.T) {
	root := t.TempDir()
	launches := filepath.Join(root, "launches.txt")
	script := writeFakeClaude(t, strings.NewReplacer("__LAUNCHES__", launches).Replace(`
printf '%s\n' "$*" >> "__LAUNCHES__"
printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'
`))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	first := driveExternalTask(t, a, handle.TaskID)
	if first.ExternalSession == nil || first.ExternalSession.SessionID == "" || !first.ExternalSession.SessionEstablished {
		t.Fatalf("first attempt binding = %+v", first.ExternalSession)
	}
	oldSessionID := first.ExternalSession.SessionID
	// A run that died before Initialize acknowledged the session leaves a
	// generated id with no transcript. Resuming it with --resume would fail
	// every follow-up, so the next attempt must start a fresh CLI session.
	a.subs.mu.Lock()
	record := a.subs.taskRecords[handle.TaskID]
	record.ExternalSession.SessionEstablished = false
	record.ExternalSession.ProcessEnded = true
	a.subs.mu.Unlock()
	if _, err := a.rehydrateExternalTask(record, "Fresh input.", nil); err != nil {
		t.Fatalf("fresh-session rehydrate: %v", err)
	}
	second := driveExternalTask(t, a, handle.TaskID)
	if second.Attempt != 2 || second.State != string(SubAgentStateCompleted) {
		t.Fatalf("fresh-session attempt = %+v", second)
	}
	data, err := os.ReadFile(launches)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("launch count = %d, want 2: %s", len(lines), data)
	}
	if strings.Contains(lines[1], "--resume") {
		t.Fatalf("unestablished session was resumed: %s", lines[1])
	}
	newSessionID := second.ExternalSession.SessionID
	if newSessionID == "" || newSessionID == oldSessionID {
		t.Fatalf("fresh session id = %q, old %q", newSessionID, oldSessionID)
	}
	if !strings.Contains(lines[1], "--session-id "+newSessionID) {
		t.Fatalf("fresh launch did not bind a new session: %s", lines[1])
	}
}

func TestExternalFailedSettlementRunsQueuedInput(t *testing.T) {
	root := t.TempDir()
	launches := filepath.Join(root, "launches.txt")
	inputs := filepath.Join(root, "inputs.txt")
	release := filepath.Join(root, "release")
	script := writeFakeClaude(t, strings.NewReplacer(
		"__LAUNCHES__", launches, "__INPUTS__", inputs, "__RELEASE__", release,
	).Replace(`
printf '%s\n' "$*" >> "__LAUNCHES__"
printf '%s\n' "$prompt" >> "__INPUTS__"
launches=$(wc -l < "__LAUNCHES__")
if [ "$launches" -eq 1 ]; then
  printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"boom"}'
  while [ ! -f "__RELEASE__" ]; do sleep 0.01; done
  exit 1
fi
printf '%s\n' '{"type":"result","subtype":"success","result":"Recovered."}'
`))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(handle.AgentID).(*externalWorker)
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.promptMu.Lock()
		sealed := w.inputClosed
		w.promptMu.Unlock()
		if sealed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("input did not seal")
		}
		time.Sleep(time.Millisecond)
	}
	next, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "Queued input.", string(SubAgentMailboxKindDecisionRequired))
	if err != nil || next.Status != "queued" {
		t.Fatalf("sealed followup = %+v %v", next, err)
	}
	if len(a.sem) != 1 || a.taskRecordByTaskID(handle.TaskID).Attempt != 1 {
		t.Fatal("slot released or new attempt started before process exit")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(15 * time.Second)
	var record *DurableTaskRecord
	for {
		record = a.taskRecordByTaskID(handle.TaskID)
		if record != nil && record.Attempt == 2 && isTerminalSubAgentState(SubAgentState(record.State)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued input did not run after failed settlement; record=%+v", record)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, eventErr := a.nextEvent(ctx)
		cancel()
		if eventErr != nil {
			t.Fatalf("wait for queued rerun: %v", eventErr)
		}
		a.dispatch(event)
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("queued rerun = %+v, want completed", record)
	}
	data, err := os.ReadFile(inputs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "Queued input.") != 1 {
		t.Fatalf("queued input delivery = %s", data)
	}
}

func TestExternalTaskResumesAfterProvenSupervisorReceipt(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Initial input."})
	if err != nil {
		t.Fatal(err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.ExternalSession == nil || record.ExternalSession.SessionID == "" {
		t.Fatalf("task has no session binding: %+v", record)
	}
	// The supervisor leaves a receipt for every run, including one Chord
	// settled itself: that is what covers a crash between the spawn and the
	// persist of the exit.
	if !externalsupervisor.ValidRunID(record.ExternalSession.RunID) {
		t.Fatalf("task has no supervisor run id: %+v", record.ExternalSession)
	}
	receipt, err := externalsupervisor.LoadReceipt(a.externalSupervisorReceiptDir(), record.ExternalSession.RunID)
	if err != nil {
		t.Fatalf("load receipt of the last run: %v", err)
	}
	if !receipt.Cleanup.Proven {
		t.Fatalf("receipt cleanup = %+v, want proven", receipt.Cleanup)
	}
	// A crash after the last durable update: the process outlived it and the
	// supervisor tore the group down without Chord seeing the report.
	record.ExternalSession.ProcessEnded = false
	w, err := a.rehydrateExternalTask(record, "Followup input.", nil)
	if err != nil {
		t.Fatalf("proven receipt did not allow resume: %v", err)
	}
	if w.currentSessionID() != record.ExternalSession.SessionID {
		t.Fatalf("resumed session = %q, want %q", w.currentSessionID(), record.ExternalSession.SessionID)
	}
	settled := driveExternalTask(t, a, handle.TaskID)
	if settled.Attempt != 2 || settled.State != string(SubAgentStateCompleted) {
		t.Fatalf("resumed task = %+v", settled)
	}
	if settled.ExternalSession.RunID == record.ExternalSession.RunID {
		t.Fatal("resumed run reused the previous run id")
	}
}

func TestExternalTaskRefusesResumeWithoutProvenRunReceipt(t *testing.T) {
	cases := []struct {
		name       string
		emptyRunID bool
		prepare    func(t *testing.T, dir, runID string)
	}{
		{name: "missing receipt"},
		{name: "empty run id", emptyRunID: true},
		{name: "cleanup unproven", prepare: func(t *testing.T, dir, runID string) {
			writeSupervisorReceipt(t, dir, externalsupervisor.Receipt{
				Version: 1, RunID: runID,
				Cleanup: externalsupervisor.Cleanup{Proven: false},
			})
		}},
		{name: "receipt of another run", prepare: func(t *testing.T, dir, runID string) {
			other, err := externalsupervisor.NewRunID()
			if err != nil {
				t.Fatal(err)
			}
			writeSupervisorReceipt(t, dir, externalsupervisor.Receipt{
				Version: 1, RunID: other,
				Cleanup: externalsupervisor.Cleanup{Proven: true},
			})
		}},
		{name: "unsupported receipt version", prepare: func(t *testing.T, dir, runID string) {
			writeSupervisorReceipt(t, dir, externalsupervisor.Receipt{
				Version: 99, RunID: runID,
				Cleanup: externalsupervisor.Cleanup{Proven: true},
			})
		}},
		{name: "receipt names another run", prepare: func(t *testing.T, dir, runID string) {
			other, err := externalsupervisor.NewRunID()
			if err != nil {
				t.Fatal(err)
			}
			writeSupervisorReceipt(t, dir, externalsupervisor.Receipt{
				Version: 1, RunID: other,
				Cleanup: externalsupervisor.Cleanup{Proven: true},
			})
			// The receipt that was just written is named after its own run;
			// move it to the bound run's name to test the content check.
			if err := os.Rename(filepath.Join(dir, other+".json"), filepath.Join(dir, runID+".json")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed receipt", prepare: func(t *testing.T, dir, runID string) {
			if err := os.WriteFile(filepath.Join(dir, runID+".json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			script := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'`)
			a := newTestMainAgent(t, root)
			configureExternalTestRuntime(t, a, script, `"*": allow`)
			handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Input."})
			if err != nil {
				t.Fatal(err)
			}
			record := driveExternalTask(t, a, handle.TaskID)
			record.ExternalSession.ProcessEnded = false
			// The run that actually executed has its own proven receipt.
			// Refusal has to be decided by the run the binding persists, so
			// every case binds a run id that never ran.
			runID, err := externalsupervisor.NewRunID()
			if err != nil {
				t.Fatal(err)
			}
			if tc.emptyRunID {
				runID = ""
			}
			record.ExternalSession.RunID = runID
			if tc.prepare != nil {
				dir := a.externalSupervisorReceiptDir()
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				tc.prepare(t, dir, runID)
			}
			if _, err := a.rehydrateExternalTask(record, "New input.", nil); err == nil {
				t.Fatal("resume without a proven receipt of the bound run was allowed")
			}
			if len(a.sem) != 0 {
				t.Fatal("failed resume leaked a runtime slot")
			}
		})
	}
}

func TestExternalTaskRefusesUnsafeResume(t *testing.T) {
	for _, unsafe := range []string{"process", "outcome", "prompt"} {
		t.Run(unsafe, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			script := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'`)
			configureExternalTestRuntime(t, a, script, `"*": allow`)
			h, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Input."})
			if err != nil {
				t.Fatal(err)
			}
			record := driveExternalTask(t, a, h.TaskID)
			switch unsafe {
			case "process":
				// The run actually executed, so its receipt proves the exit.
				// Unproven termination is a run that left no receipt at all.
				record.ExternalSession.ProcessEnded = false
				runID, err := externalsupervisor.NewRunID()
				if err != nil {
					t.Fatal(err)
				}
				record.ExternalSession.RunID = runID
			case "outcome":
				record.ExternalSession.Inputs[0].State = message.ToolRecoveryStateOutcomeUnknown
			case "prompt":
				a.agentConfigs["external-worker"].SystemPrompt = "Changed instructions."
			}
			if _, err := a.rehydrateExternalTask(record, "New input.", nil); err == nil {
				t.Fatal("unsafe external task resumed")
			}
			if len(a.sem) != 0 {
				t.Fatal("failed resume leaked a runtime slot")
			}
		})
	}
}
