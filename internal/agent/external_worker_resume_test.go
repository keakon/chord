package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
				record.ExternalSession.ProcessEnded = false
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
