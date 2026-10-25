package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// TestExternalWorkerReportsActivityPhases pins the phase feed the status bar
// depends on. The external process reports no activity of its own, so without
// these transitions focused input can only show an optimistic "connecting"
// timer anchored on the send time.
func TestExternalWorkerReportsActivityPhases(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"external answer"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"README.md"}}]}}'
printf '%s\n' '{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"Read","input":{"file_path":"README.md"},"tool_use_id":"tool-1"}}'
IFS= read -r permission_response
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"fixture contents"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"external answer"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "read the fixture",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}

	var phases []ActivityType
	for _, evt := range drainAgentEvents(a.outputCh) {
		activity, ok := evt.(AgentActivityEvent)
		if !ok || activity.AgentID != handle.AgentID {
			continue
		}
		phases = append(phases, activity.Type)
	}
	want := []ActivityType{
		ActivityPreparing, ActivityWaitingToken, ActivityStreaming,
		ActivityExecuting, ActivityWaitingToken, ActivityIdle,
	}
	if !containsActivitySubsequence(phases, want) {
		t.Fatalf("activity phases = %v, want subsequence %v", phases, want)
	}
}

func containsActivitySubsequence(phases, want []ActivityType) bool {
	next := 0
	for _, phase := range phases {
		if next < len(want) && phase == want[next] {
			next++
		}
	}
	return next == len(want)
}

func TestRunningExternalToolsCountsOnlyStartedUnsettledCalls(t *testing.T) {
	w := &externalWorker{
		toolNames: map[string]string{
			"pending": toolname.Read, "first": toolname.Read, "second": toolname.Read,
		},
		toolBindings: map[string]*externalToolBinding{
			"pending": {}, "first": {started: true}, "second": {started: true},
			"settled": {started: true, settled: true}, "denied": {settled: true},
		},
	}
	check := func(want int) {
		t.Helper()
		w.externalToolStateMu.Lock()
		defer w.externalToolStateMu.Unlock()
		if got := w.runningExternalToolsLocked(); got != want {
			t.Fatalf("running count = %d, want %d", got, want)
		}
	}
	check(2)
	w.toolBindings["first"].settled = true
	w.forgetToolName("first")
	check(1)
	w.toolBindings["pending"].started = true
	check(2)
	w.toolBindings["second"].settled = true
	w.forgetToolName("second")
	check(1)
	w.toolBindings["pending"].settled = true
	w.forgetToolName("pending")
	check(0)
}
