package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/tools"
)

// watchExternalConfirmEvents drains the TUI output channel, optionally answers
// the worker's confirmation, and reports the terminal resolution. The external
// permission wait blocks a worker goroutine, so the event loop keeps pumping in
// the test goroutine while this helper owns outputCh.
func watchExternalConfirmEvents(a *MainAgent, agentID string, answer bool) (<-chan ConfirmRequestEvent, <-chan ConfirmResolvedEvent) {
	requests := make(chan ConfirmRequestEvent, 1)
	resolutions := make(chan ConfirmResolvedEvent, 1)
	go func() {
		deadline := time.NewTimer(15 * time.Second)
		defer deadline.Stop()
		var requestID string
		for requestID == "" {
			select {
			case event := <-a.outputCh:
				if request, ok := event.(ConfirmRequestEvent); ok && request.AgentID == agentID {
					requestID = request.RequestID
					requests <- request
					if answer {
						a.ResolveConfirm("allow", request.ArgsJSON, "", "", request.RequestID)
					}
				}
			case <-deadline.C:
				return
			}
		}
		for {
			select {
			case event := <-a.outputCh:
				if resolved, ok := event.(ConfirmResolvedEvent); ok && resolved.RequestID == requestID {
					resolutions <- resolved
					return
				}
			case <-deadline.C:
				return
			}
		}
	}()
	return requests, resolutions
}

// startConfirmCycleTask launches an external worker whose fake CLI asks the
// host to authorize one Read call, records the decision it received, and then
// finishes the turn. toolResult is the user-tool_result frame the CLI reports
// for the call: an authorized read reports content, a denied one an error.
func startConfirmCycleTask(t *testing.T, permissionYAML, toolResult string) (*MainAgent, tools.TaskHandle, string) {
	t.Helper()
	root := t.TempDir()
	decisionFile := filepath.Join(root, "decision.txt")
	body := strings.ReplaceAll(`
printf '%s\n' '{"type":"system","subtype":"init","session_id":"77777777-7777-4777-8777-777777777771"}'
printf '%s\n' '{"type":"control_request","request_id":"perm-cycle","request":{"subtype":"can_use_tool","tool_name":"Read","input":{"file_path":"README.md"},"tool_use_id":"tool-cycle"}}'
IFS= read -r line
printf '%s' "$line" > "__DECISION_FILE__"
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-cycle",__TOOL_RESULT__}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"confirm cycle done"}'
`, "__DECISION_FILE__", decisionFile)
	body = strings.Replace(body, "__TOOL_RESULT__", toolResult, 1)
	script := writeFakeClaude(t, body)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, permissionYAML)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "read with a confirmation",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	return a, handle, decisionFile
}

const externalConfirmAuthorizedToolResult = `"content":"fixture contents"`
const externalConfirmDeniedToolResult = `"content":"denied","is_error":true`

// An external can_use_tool ask surfaces as a confirm request owned by the
// worker instance, and answering it ends the cycle with a resolved event that
// still names the worker, before the CLI receives the allow decision.
func TestExternalConfirmAnsweredCycleCarriesWorkerIdentity(t *testing.T) {
	a, handle, _ := startConfirmCycleTask(t, `"*": ask`, externalConfirmAuthorizedToolResult)
	requests, resolutions := watchExternalConfirmEvents(a, handle.AgentID, true)

	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (closed_reason=%q summary=%q)", record.State, record.ClosedReason, record.LastSummary)
	}
	select {
	case request := <-requests:
		if request.AgentID != handle.AgentID || request.ToolName != tools.NameRead {
			t.Fatalf("confirm request = %+v, want %q owned by %v", request, tools.NameRead, handle.AgentID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("external permission did not request a confirmation")
	}
	select {
	case resolved := <-resolutions:
		if resolved.AgentID != handle.AgentID || resolved.Outcome != ConfirmOutcomeApproved || resolved.ToolName != tools.NameRead {
			t.Fatalf("confirm resolution = %+v, want %q approved for %v", resolved, tools.NameRead, handle.AgentID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answered confirmation was not resolved with an event")
	}
}

// A confirmation nobody answers times out, auto-denies, and still ends with a
// terminal event naming the worker: the headless control plane forwards it as
// confirm_resolved so integrations can retire the card.
func TestExternalConfirmTimeoutResolvesWithWorkerIdentity(t *testing.T) {
	original := externalPermissionTimeout
	externalPermissionTimeout = 300 * time.Millisecond
	t.Cleanup(func() { externalPermissionTimeout = original })

	a, handle, decisionFile := startConfirmCycleTask(t, `"*": ask`, externalConfirmDeniedToolResult)
	requests, resolutions := watchExternalConfirmEvents(a, handle.AgentID, false)

	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed after a denied read", record.State)
	}
	select {
	case <-requests:
	case <-time.After(5 * time.Second):
		t.Fatal("external permission did not request a confirmation")
	}
	select {
	case resolved := <-resolutions:
		if resolved.AgentID != handle.AgentID || resolved.Outcome != ConfirmOutcomeTimeout {
			t.Fatalf("confirm resolution = %+v, want a timeout for %v", resolved, handle.AgentID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed-out confirmation was not resolved with an event")
	}
	decision, err := os.ReadFile(decisionFile)
	if err != nil {
		t.Fatalf("read permission decision: %v", err)
	}
	if !strings.Contains(string(decision), `"behavior":"deny"`) {
		t.Fatalf("control response = %s, want deny", decision)
	}
}

// Cancelling the worker while its confirmation is pending resolves the wait
// with the cancelled outcome, so a client that still shows the request learns
// it is no longer actionable.
func TestExternalConfirmCancelResolvesWithWorkerIdentity(t *testing.T) {
	a, handle, _ := startConfirmCycleTask(t, `"*": ask`, externalConfirmAuthorizedToolResult)
	requests, resolutions := watchExternalConfirmEvents(a, handle.AgentID, false)

	var request ConfirmRequestEvent
	select {
	case request = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("external permission did not request a confirmation")
	}
	runtime := a.subs.runtime(handle.AgentID)
	worker, ok := runtime.(*externalWorker)
	if !ok || worker == nil {
		t.Fatalf("runtime %T is not an externalWorker", runtime)
	}
	if !worker.requestUserCancel("stopped by test") {
		t.Fatal("user cancel was not signalled")
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCancelled) {
		t.Fatalf("task state = %q, want cancelled", record.State)
	}
	select {
	case resolved := <-resolutions:
		if resolved.AgentID != handle.AgentID || resolved.Outcome != ConfirmOutcomeCancelled || resolved.RequestID != request.RequestID {
			t.Fatalf("confirm resolution = %+v, want %q cancelled for %v", resolved, request.RequestID, handle.AgentID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled confirmation was not resolved with an event")
	}
}

// An external AskUserQuestion travels the full host cycle: the worker's run
// loop delivers the can_use_tool request, the question becomes pending in the
// host question registry, and the user's answers ride the allow decision back
// to the CLI as updatedInput.answers.
func TestExternalQuestionFullCycleThroughRunLoop(t *testing.T) {
	root := t.TempDir()
	decisionFile := filepath.Join(root, "decision.txt")
	script := writeFakeClaude(t, strings.ReplaceAll(`
printf '%s\n' '{"type":"system","subtype":"init","session_id":"88888888-8888-4888-8888-888888888888"}'
printf '%s\n' '{"type":"control_request","request_id":"perm-question","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"header":"Choice","question":"Choose an option","options":[{"label":"Alpha","description":"First"},{"label":"Beta","description":"Second"}]},{"header":"Text","question":"Provide a value","options":[{"label":"Default","description":"Default value"}]}]},"tool_use_id":"tool-question"}}'
IFS= read -r line
printf '%s' "$line" > "__DECISION_FILE__"
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-question","content":"answered"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"question answered"}'
`, "__DECISION_FILE__", decisionFile))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "ask a structured question",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	answered := 0
	var record *DurableTaskRecord
	deadline := time.After(15 * time.Second)
	for record == nil || !isTerminalSubAgentState(SubAgentState(record.State)) {
		select {
		case event := <-a.eventCh:
			a.dispatch(event)
			for id, q := range a.questions.records {
				if !q.pending() {
					continue
				}
				operation := QuestionOperation{Operation: QuestionOpAnswer, QuestionID: id, Answers: []string{"0"}}
				if q.Item.Header == "Text" {
					operation.Answers = []string{"A custom value"}
					operation.Custom = true
				}
				receipt := applyTestQuestion(t, a, operation, time.Time{})
				if !receipt.Accepted {
					t.Fatalf("answer for %q rejected: %+v", q.Item.Header, receipt)
				}
				answered++
			}
		case <-deadline:
			t.Fatalf("timed out waiting for the external question cycle; answered=%d record=%+v", answered, a.taskRecordByTaskID(handle.TaskID))
		}
		record = a.taskRecordByTaskID(handle.TaskID)
	}
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (summary=%q)", record.State, record.LastSummary)
	}
	if answered != 2 {
		t.Fatalf("answered questions = %d, want 2", answered)
	}
	decision, err := os.ReadFile(decisionFile)
	if err != nil {
		t.Fatalf("read permission decision: %v", err)
	}
	for _, want := range []string{`"behavior":"allow"`, `"Choose an option":"Alpha"`, `"Provide a value":"A custom value"`} {
		if !strings.Contains(string(decision), want) {
			t.Fatalf("control response = %s, want it to contain %s", decision, want)
		}
	}
}
