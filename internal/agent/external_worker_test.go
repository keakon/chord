package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func writeFakeClaude(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-claude")
	script := "#!/bin/sh\n" + `
IFS= read -r init
case "$init" in *'"subtype":"initialize"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize","response":{}}}'
IFS= read -r prompt
` + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func configureExternalTestRuntime(t *testing.T, a *MainAgent, command string, permissionYAML string) {
	t.Helper()
	def := &config.AgentConfig{
		Name:        "external-worker",
		Description: "external worker",
		Mode:        config.AgentModeSubAgent,
		Backend: &config.BackendConfig{
			Kind:    config.BackendKindClaudeCode,
			Command: command,
		},
	}
	if permissionYAML != "" {
		def.Permission = parsePermissionNode(t, permissionYAML)
	}
	a.agentConfigs = map[string]*config.AgentConfig{"external-worker": def}
	a.activeConfig = &config.AgentConfig{
		Name:       "builder",
		Delegation: config.DelegationConfig{MaxChildren: 10, MaxDepth: 2},
	}
	a.rebuildRuleset()
}

func driveExternalTask(t *testing.T, a *MainAgent, taskID string) *DurableTaskRecord {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if record := a.taskRecordByTaskID(taskID); record != nil && isTerminalSubAgentState(SubAgentState(record.State)) {
			return record
		}
		if time.Now().After(deadline) {
			record := a.taskRecordByTaskID(taskID)
			t.Fatalf("timed out waiting for external task %s terminal state; record=%+v", taskID, record)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, err := a.nextEvent(ctx)
		cancel()
		if err != nil {
			t.Fatalf("next event for external task %s: %v", taskID, err)
		}
		a.dispatch(event)
	}
}

func TestExternalWorkerOneShotCompletesTask(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"external done"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"external done"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "investigate the fixture",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if handle.Status != "started" {
		t.Fatalf("handle.Status = %q, want started", handle.Status)
	}
	runtime := a.subs.runtime(handle.AgentID)
	worker, ok := runtime.(*externalWorker)
	if !ok || worker == nil {
		t.Fatalf("runtime %T is not an externalWorker", runtime)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (closed_reason=%q summary=%q)", record.State, record.ClosedReason, record.LastSummary)
	}
	if record.ExternalSession == nil {
		t.Fatal("completed external task has no ExternalSession record")
	}
	if !externalclaude.ValidSessionID(record.ExternalSession.SessionID) {
		t.Fatalf("ExternalSession.SessionID = %q, want a UUID", record.ExternalSession.SessionID)
	}
	if record.ExternalSession.Backend != config.BackendKindClaudeCode {
		t.Fatalf("ExternalSession.Backend = %q, want %q", record.ExternalSession.Backend, config.BackendKindClaudeCode)
	}
	if record.LastSummary != "external done" {
		t.Fatalf("LastSummary = %q, want external done", record.LastSummary)
	}
	if record.LatestSettlement == nil || record.LatestSettlement.Outcome != string(SubAgentStateCompleted) {
		t.Fatalf("LatestSettlement = %+v, want completed settlement", record.LatestSettlement)
	}
	select {
	case <-worker.done:
	case <-time.After(5 * time.Second):
		t.Fatal("external worker did not finish its run loop")
	}
}

func TestExternalWorkerPermissionAllowDoesNotAskUser(t *testing.T) {
	root := t.TempDir()
	decisionFile := filepath.Join(root, "decision.txt")
	script := writeFakeClaude(t, strings.ReplaceAll(`
printf '%s\n' '{"type":"system","subtype":"init","session_id":"22222222-2222-4222-8222-222222222222"}'
printf '%s\n' '{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"Read","input":{"file_path":"README.md"},"tool_use_id":"tool-1"}}'
IFS= read -r line
printf '%s' "$line" > "__DECISION_FILE__"
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"fixture contents"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"permission processed"}'
`, "__DECISION_FILE__", decisionFile))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "read something",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}
	decision, err := os.ReadFile(decisionFile)
	if err != nil {
		t.Fatalf("read permission decision: %v", err)
	}
	if !strings.Contains(string(decision), `"behavior":"allow"`) {
		t.Fatalf("control response = %s, want allow", decision)
	}
}

func TestExternalWorkerPermissionDenyDoesNotAskUser(t *testing.T) {
	root := t.TempDir()
	decisionFile := filepath.Join(root, "decision.txt")
	script := writeFakeClaude(t, strings.ReplaceAll(`
printf '%s\n' '{"type":"system","subtype":"init","session_id":"33333333-3333-4333-8333-333333333333"}'
printf '%s\n' '{"type":"control_request","request_id":"perm-2","request":{"subtype":"can_use_tool","tool_name":"Write","input":{"file_path":"blocked.txt","content":"x"},"tool_use_id":"tool-2"}}'
IFS= read -r line
printf '%s' "$line" > "__DECISION_FILE__"
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-2","content":"denied","is_error":true}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"permission denied"}'
`, "__DECISION_FILE__", decisionFile))
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": deny`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "write something",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}
	decision, err := os.ReadFile(decisionFile)
	if err != nil {
		t.Fatalf("read permission decision: %v", err)
	}
	if !strings.Contains(string(decision), `"behavior":"deny"`) {
		t.Fatalf("control response = %s, want deny", decision)
	}
}

func TestExternalWorkerConsumesQueuedPromptInNextAttempt(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
count_file=turns.txt
if [ -f "$count_file" ]; then
  i=$(cat "$count_file")
else
  i=0
fi
printf '%s\n' '{"type":"system","subtype":"init","session_id":"44444444-4444-4444-8444-444444444444"}'
while :; do
  i=$((i+1))
  printf '%s\n' "$i" > "$count_file"
  sleep 0.2
  printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"turn-$i\"}]}}"
  printf '%s\n' "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"turn-$i\"}"
  IFS= read -r line || break
done
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "queued follow-up task",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if _, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "second turn", string(SubAgentMailboxKindDecisionRequired)); err != nil {
		t.Fatalf("sendMessageToSubAgentNow: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (summary=%q)", record.State, record.LastSummary)
	}
	if record.LastSummary != "turn-2" {
		t.Fatalf("LastSummary = %q, want turn-2 (the queued prompt must have run in the resumed attempt)", record.LastSummary)
	}
}

func TestExternalWorkerStreamsPartialTextAndEndsSegment(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"44444444-4444-4444-8444-444444444444"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"complete"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	a.agentConfigs["external-worker"].Backend.LaunchArgs = []string{"--include-partial-messages"}

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "stream a partial response",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	a.setFocusedTaskID(handle.TaskID)
	t.Cleanup(func() { a.setFocusedTaskID("") })
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}
	var sawDelta, sawCommit, sawEnd bool
	for len(a.outputCh) > 0 {
		switch event := (<-a.outputCh).(type) {
		case StreamTextEvent:
			if event.Text == "partial" && event.AgentID == handle.AgentID && event.RequestSeq != 0 {
				sawDelta = true
			}
		case StreamTextCommitEvent:
			if event.Text == "complete" && event.AgentID == handle.AgentID {
				sawCommit = true
			}
		case StreamSegmentEndedEvent:
			if event.AgentID == handle.AgentID && event.RequestSeq != 0 {
				sawEnd = true
			}
		}
	}
	if !sawDelta || !sawCommit || !sawEnd {
		t.Fatalf("stream events: delta=%v commit=%v end=%v", sawDelta, sawCommit, sawEnd)
	}
}

func TestExternalWorkerKeepsStreamSegmentsAlignedWithAssistantMessages(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"55555555-5555-4555-8555-555555555555"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first "}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first answer"},{"type":"tool_use","id":"toolu_first","name":"Read","input":{"file_path":"README.md"}}]}}'
printf '%s\n' '{"type":"control_request","request_id":"perm-1","request":{"subtype":"can_use_tool","tool_name":"Read","input":{"file_path":"README.md"},"tool_use_id":"toolu_first"}}'
IFS= read -r line
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_first","content":"ok"}]}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"second answer"}}}'
printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"second answer"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"second answer"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	a.agentConfigs["external-worker"].Backend.LaunchArgs = []string{"--include-partial-messages"}

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "stream two assistant messages",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	a.setFocusedTaskID(handle.TaskID)
	t.Cleanup(func() { a.setFocusedTaskID("") })
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}

	type streamStep struct {
		kind   string
		text   string
		seq    uint64
		isTool bool
	}
	var steps []streamStep
	for len(a.outputCh) > 0 {
		switch event := (<-a.outputCh).(type) {
		case ToolCallStartEvent:
			if event.ID == "toolu_first" {
				steps = append(steps, streamStep{kind: "tool", isTool: true})
			}
		case StreamTextCommitEvent:
			if event.AgentID == handle.AgentID {
				steps = append(steps, streamStep{kind: "commit", text: event.Text, seq: event.RequestSeq})
			}
		case StreamSegmentEndedEvent:
			if event.AgentID == handle.AgentID {
				steps = append(steps, streamStep{kind: "end", seq: event.RequestSeq})
			}
		}
	}
	var commits []streamStep
	toolAt := -1
	for i, step := range steps {
		if step.isTool {
			toolAt = i
		}
		if step.kind == "commit" {
			commits = append(commits, step)
		}
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %+v, want one per assistant message", commits)
	}
	if commits[0].text != "first answer" || commits[1].text != "second answer" {
		t.Fatalf("commit texts = %q, %q; want first/second answer", commits[0].text, commits[1].text)
	}
	if commits[0].seq == commits[1].seq {
		t.Fatalf("both messages committed into request seq %d; want a fresh segment per message", commits[0].seq)
	}
	if toolAt < 0 {
		t.Fatal("tool call event missing")
	}
	firstCommitAt, secondCommitAt := -1, -1
	for i, step := range steps {
		if step.kind == "commit" && step.text == "first answer" {
			firstCommitAt = i
		}
		if step.kind == "commit" && step.text == "second answer" {
			secondCommitAt = i
		}
	}
	if !(firstCommitAt < toolAt && toolAt < secondCommitAt) {
		t.Fatalf("stream order = %+v; want first text, tool card, second text", steps)
	}
}

func TestExternalWorkerDoesNotReusePreviousTurnText(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
count_file=turns.txt
if [ -f "$count_file" ]; then
  i=$(cat "$count_file")
else
  i=0
fi
printf '%s\n' '{"type":"system","subtype":"init","session_id":"66666666-6666-4666-8666-666666666666"}'
while :; do
  i=$((i+1))
  printf '%s\n' "$i" > "$count_file"
  sleep 0.2
  if [ "$i" -eq 1 ]; then
    printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first turn"}]}}'
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"first turn"}'
  else
    printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":""}'
  fi
  IFS= read -r line || break
done
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "isolate turn summaries",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	if _, err := a.sendMessageToSubAgentNow("", "", handle.TaskID, "second turn", string(SubAgentMailboxKindDecisionRequired)); err != nil {
		t.Fatalf("sendMessageToSubAgentNow: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed", record.State)
	}
	if record.LastSummary == "first turn" {
		t.Fatal("second turn reused the previous turn's assistant text")
	}
}

func TestExternalWorkerRejectsUnknownToolResult(t *testing.T) {
	root := t.TempDir()
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"55555555-5555-4555-8555-555555555555"}'
printf '%s\n' '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"unknown-tool","content":"unexpected"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"should fail"}'
`)
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "reject an uncorrelated result",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateFailed) {
		t.Fatalf("task state = %q, want failed", record.State)
	}
	if !strings.Contains(record.LastSummary, "no matching tool call") {
		t.Fatalf("LastSummary = %q, want protocol failure", record.LastSummary)
	}
}

func TestExternalWorkerAcceptsUnpinnedVersion(t *testing.T) {
	root := t.TempDir()
	script := filepath.Join(t.TempDir(), "unprobeable-claude")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '2.1.308 (Claude Code)\n'
  exit 0
fi
IFS= read -r init
case "$init" in *'"subtype":"initialize"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"chord-initialize","response":{}}}'
IFS= read -r prompt
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"external done"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newTestMainAgent(t, root)
	configureExternalTestRuntime(t, a, script, `"*": allow`)

	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		Description: "newer cli",
		AgentType:   "external-worker",
	})
	if err != nil {
		t.Fatalf("CreateSubAgent: %v", err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateCompleted) {
		t.Fatalf("task state = %q, want completed (closed_reason=%q summary=%q)", record.State, record.ClosedReason, record.LastSummary)
	}
	if record.LastSummary != "external done" {
		t.Fatalf("LastSummary = %q, want external done", record.LastSummary)
	}
}

func TestExternalWorkerNamesUnmanagedInitTool(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"11111111-1111-4111-8111-111111111111","tools":["Read","FutureTool"]}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"should not run"}'
`)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Input."})
	if err != nil {
		t.Fatal(err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateFailed) || !strings.Contains(record.LastSummary, "FutureTool") {
		t.Fatalf("task state=%q summary=%q, want failed with the unmanaged tool name", record.State, record.LastSummary)
	}
}

func TestExternalWorkerKeepsResultErrorOnNonzeroExit(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, `
printf '%s\n' '{"type":"result","subtype":"error","is_error":true,"result":"No conversation found"}'
exit 1
`)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Input."})
	if err != nil {
		t.Fatal(err)
	}
	record := driveExternalTask(t, a, handle.TaskID)
	if record.State != string(SubAgentStateFailed) {
		t.Fatalf("task state = %q, want failed", record.State)
	}
	if !strings.Contains(record.LastSummary, "No conversation found") || !strings.Contains(record.LastSummary, "code 1") {
		t.Fatalf("LastSummary = %q, want the result error and the exit status", record.LastSummary)
	}
}

func TestExternalCheckoutCompletionReportsOnlyCheckoutIdentity(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "tracked.txt")
	run("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-qm", "base")
	baseline := captureExternalCheckoutBaseline(root)
	if baseline == nil || baseline.Dirty || baseline.Branch == "" {
		t.Fatalf("baseline = %+v", baseline)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	binding := WorkDirState{Path: root, WorktreeID: "main", Generation: 1}
	env := externalCheckoutCompletion(root, baseline, binding, "done")
	if len(env.ActualFilesChanged) != 0 || !env.FileAttributionIncomplete {
		t.Fatalf("completion must not attribute files: %+v", env)
	}
	if env.Worktree == nil || env.Worktree.Base != baseline.Head || env.Worktree.Branch != baseline.Branch || env.Worktree.DiffStat == "" {
		t.Fatalf("completion worktree = %+v", env.Worktree)
	}
	run("add", "tracked.txt", "new.txt")
	run("-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "-qm", "changes")
	committed := externalCheckoutCompletion(root, baseline, binding, "done")
	if len(committed.ActualFilesChanged) != 0 || committed.Worktree.DiffStat == "" {
		t.Fatalf("a worker's own commit must still show in the diff stat: %+v", committed)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("already dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty := captureExternalCheckoutBaseline(root)
	if dirty == nil || !dirty.Dirty {
		t.Fatalf("dirty baseline = %+v", dirty)
	}
	final := externalCheckoutCompletion(root, dirty, binding, "done")
	if len(final.ActualFilesChanged) != 0 || final.Worktree.DiffError == "" || final.Worktree.DiffStat != "" {
		t.Fatalf("dirty startup must withhold the diff stat: %+v", final)
	}
}

func TestExternalCheckoutCompletionWithholdsDiffWhenGitStatusFails(t *testing.T) {
	root := t.TempDir()
	fakeGit := func(body string) {
		dir := t.TempDir()
		path := filepath.Join(dir, "git")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	fakeGit(`
cmd="$3"
case "$cmd" in
  rev-parse) printf '0123456789abcdef0123456789abcdef01234567\nmain\n'; exit 0 ;;
  status) exit 1 ;;
esac
exit 1
`)
	baseline := captureExternalCheckoutBaseline(root)
	if baseline == nil || !baseline.StatusUnknown || baseline.Dirty {
		t.Fatalf("baseline = %+v, want head with unknown status", baseline)
	}
	binding := WorkDirState{Path: root, WorktreeID: "main", Generation: 1}
	env := externalCheckoutCompletion(root, baseline, binding, "done")
	if env.Worktree == nil || env.Worktree.DiffError == "" || env.Worktree.DiffStat != "" {
		t.Fatalf("unknown git status must withhold the diff stat: %+v", env.Worktree)
	}
	if !strings.Contains(env.Worktree.DiffError, "status could not be checked") {
		t.Fatalf("DiffError = %q", env.Worktree.DiffError)
	}
}

func TestExternalCheckoutCompletionReportsUnknownStartupBranch(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(`#!/bin/sh
cmd="$3"
case "$cmd" in
  rev-parse) printf '0123456789abcdef0123456789abcdef01234567\n'; exit 0 ;;
  status) exit 0 ;;
  diff) exit 0 ;;
esac
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	baseline := captureExternalCheckoutBaseline(root)
	if baseline == nil || !baseline.BranchUnknown || baseline.Branch != "" {
		t.Fatalf("baseline = %+v, want unknown branch", baseline)
	}
	env := externalCheckoutCompletion(root, baseline, WorkDirState{Path: root, WorktreeID: "main", Generation: 1}, "done")
	if env.Worktree == nil || env.Worktree.Branch != "" {
		t.Fatalf("worktree = %+v, want no fabricated branch", env.Worktree)
	}
	found := false
	for _, limitation := range env.RemainingLimitations {
		if strings.Contains(limitation, "branch could not be determined") {
			found = true
		}
	}
	if !found {
		t.Fatalf("RemainingLimitations = %v", env.RemainingLimitations)
	}
}

func TestExternalCheckoutCompletionWithholdsDiffWhenGitDiffFails(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(`#!/bin/sh
cmd="$3"
case "$cmd" in
  rev-parse) printf '0123456789abcdef0123456789abcdef01234567\nmain\n'; exit 0 ;;
  status) exit 0 ;;
  diff) exit 1 ;;
esac
exit 1
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	baseline := captureExternalCheckoutBaseline(root)
	if baseline == nil || baseline.StatusUnknown || baseline.Dirty {
		t.Fatalf("baseline = %+v, want clean head", baseline)
	}
	binding := WorkDirState{Path: root, WorktreeID: "main", Generation: 1}
	env := externalCheckoutCompletion(root, baseline, binding, "done")
	if env.Worktree == nil || env.Worktree.DiffError == "" || env.Worktree.DiffStat != "" {
		t.Fatalf("failed git diff must withhold the diff stat: %+v", env.Worktree)
	}
}

func TestExternalCompletionRequiresProcessTermination(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'`)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Input."})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(handle.AgentID).(*externalWorker)
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not finish")
	}
	w.promptMu.Lock()
	w.processEnded = false
	w.terminationRetryDelay = time.Hour
	w.promptMu.Unlock()
	w.sessionMu.Lock()
	w.session = nil
	w.sessionMu.Unlock()
	a.closeExternalWorker(w, SubAgentStateFailed, "termination unproven", "termination unproven", nil, 0, false)
	record := a.taskRecordByTaskID(w.taskID)
	if len(a.sem) != 1 || isTerminalSubAgentState(SubAgentState(record.State)) || record.LatestSettlement != nil {
		t.Fatalf("unproven termination released or settled task before the retry budget: %+v", record)
	}
	a.closeExternalWorker(w, SubAgentStateFailed, "termination unproven", "termination unproven", nil, 0, true)
	record = a.taskRecordByTaskID(w.taskID)
	if len(a.sem) != 0 || record.State != string(SubAgentStateFailed) || record.ExternalSession == nil ||
		record.ExternalSession.ProcessEnded || !strings.Contains(record.LastSummary, "released") {
		t.Fatalf("unproven termination was not released safely: slot=%d record=%+v", len(a.sem), record)
	}
	for len(a.outputCh) > 0 {
		if event, ok := (<-a.outputCh).(AgentNotifyEvent); ok && event.Kind == string(SubAgentMailboxKindCompleted) {
			t.Fatal("unproven termination emitted a completed mailbox")
		}
	}
}

func TestExternalUnprovenTerminationRetriesThenReleases(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, `printf '%s\n' '{"type":"result","subtype":"success","result":"Done."}'`)
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{AgentType: "external-worker", Description: "Input."})
	if err != nil {
		t.Fatal(err)
	}
	w := a.subs.runtime(handle.AgentID).(*externalWorker)
	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not finish")
	}
	w.promptMu.Lock()
	w.processEnded = false
	w.terminationRetryAttempts = 2
	w.terminationRetryDelay = time.Millisecond
	w.promptMu.Unlock()
	w.sessionMu.Lock()
	w.session = nil
	w.sessionMu.Unlock()
	a.closeExternalWorker(w, SubAgentStateFailed, "termination unproven", "termination unproven", nil, 0, false)
	if len(a.sem) != 1 {
		t.Fatal("the slot was released before the retry budget was spent")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(a.sem) > 0 && time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		event, eventErr := a.nextEvent(ctx)
		cancel()
		if eventErr != nil {
			t.Fatalf("wait for cleanup retry: %v", eventErr)
		}
		a.dispatch(event)
	}
	record := a.taskRecordByTaskID(w.taskID)
	if len(a.sem) != 0 || record == nil || record.State != string(SubAgentStateFailed) ||
		record.ExternalSession == nil || record.ExternalSession.ProcessEnded {
		t.Fatalf("retry exhaustion did not release safely: slot=%d record=%+v", len(a.sem), record)
	}
}

func TestExternalWorkerWithoutInputFailsClosed(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.runExternalWorker()
	if state := w.State(); state != SubAgentStateFailed {
		t.Fatalf("state = %q, want failed", state)
	}
	if summary := w.LastSummary(); !strings.Contains(summary, "no input") {
		t.Fatalf("LastSummary = %q", summary)
	}
}

func newStartFailureWorker(t *testing.T, command string) (*MainAgent, *externalWorker, externalTaskInput) {
	t.Helper()
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, command, `"*": allow`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: cancel,
	})
	if err != nil {
		t.Fatal(err)
	}
	input := externalTaskInput{ID: "input-1", Content: "Input.", State: message.ToolRecoveryStateNotStarted}
	w.inputs = []externalTaskInput{input}
	a.subs.taskRecords[w.taskID] = &DurableTaskRecord{
		TaskID: w.taskID, LatestInstanceID: w.instanceID,
		ExternalSession: &ExternalTaskSession{SessionID: w.currentSessionID(), ProcessEnded: true},
	}
	return a, w, input
}

func TestExternalStartFailureWithoutCleanupProofDoesNotClaimEnded(t *testing.T) {
	_, w, input := newStartFailureWorker(t, "claude")
	original := startExternalSession
	startExternalSession = func(externalclaude.ProcessConfig) (*externalclaude.Session, error) {
		return nil, &externalclaude.StartError{Err: errors.New("injected launch failure"), CleanupProven: false}
	}
	t.Cleanup(func() { startExternalSession = original })
	_, ended, err := w.startSession(input, false)
	if err == nil {
		t.Fatal("injected launch failure unexpectedly succeeded")
	}
	if ended {
		t.Fatal("a failed launch without cleanup proof was treated as ended")
	}
}

func TestExternalStartRejectionWithReceiptProvesCleanup(t *testing.T) {
	_, w, input := newStartFailureWorker(t, filepath.Join(t.TempDir(), "missing-claude"))
	_, ended, err := w.startSession(input, false)
	if err == nil {
		t.Fatal("missing external command unexpectedly started")
	}
	if !ended {
		t.Fatal("a rejected launch proven by its receipt was not treated as ended")
	}
}

func TestExternalStartupRequiresDurableProcessState(t *testing.T) {
	for _, failPersistence := range []bool{false, true} {
		name := "state persisted before launch"
		if failPersistence {
			name = "persistence failure prevents launch"
		}
		t.Run(name, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			owner := a.currentRecoveryOwnership()
			marker := filepath.Join(t.TempDir(), "launched-state.json")
			command := filepath.Join(t.TempDir(), "fixture")
			if err := os.WriteFile(command, []byte("#!/bin/sh\n"+
				"cat '"+durableTaskRegistryPath(owner.dir)+"' > '"+marker+"'\n"+
				"IFS= read -r line\n"+
				"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true,\"result\":\"No conversation found\"}'\n"+
				"while IFS= read -r line; do exit 9; done\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			configureExternalTestRuntime(t, a, command, `"*": allow`)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w, err := a.newExternalWorker(externalWorkerConfig{
				agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
				ctx: ctx, cancel: cancel,
			})
			if err != nil {
				t.Fatal(err)
			}
			input := externalTaskInput{ID: "input-1", Content: "Input.", State: message.ToolRecoveryStateNotStarted}
			w.inputs = []externalTaskInput{input}
			w.processEnded = true
			a.subs.taskRecords[w.taskID] = &DurableTaskRecord{
				TaskID: w.taskID, LatestInstanceID: w.instanceID,
				ExternalSession: &ExternalTaskSession{SessionID: w.currentSessionID(), ProcessEnded: true},
			}
			if failPersistence {
				if err := os.MkdirAll(durableTaskRegistryPath(owner.dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err = w.startSession(input, true)
			if err == nil {
				t.Fatal("initialization unexpectedly succeeded")
			}
			data, readErr := os.ReadFile(marker)
			if failPersistence {
				if !os.IsNotExist(readErr) || w.currentSession() != nil {
					t.Fatalf("process started after storage failed: %v", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			var records []*DurableTaskRecord
			if err := json.Unmarshal(data, &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].ExternalSession.ProcessEnded ||
				len(records[0].ExternalSession.Inputs) != 1 ||
				records[0].ExternalSession.Inputs[0].State != message.ToolRecoveryStateNotStarted {
				t.Fatalf("launch did not observe the durable pre-launch state: %s", data)
			}
			if w.inputs[0].State != message.ToolRecoveryStateNotStarted || !w.currentSession().ProcessEnded() {
				t.Fatal("failed initialization submitted input or left a process")
			}
		})
	}
}

func TestExternalWorkerIgnoresNestedAgentText(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	nested := externalclaude.Event{
		Type:            externalclaude.TypeAssistant,
		ParentToolUseID: "agent-call",
		Message: &externalclaude.Message{Content: []externalclaude.ContentBlock{{
			Type: "text", Text: "nested plan",
		}}},
	}
	if terminal, _ := w.handleExternalEvent(nested); terminal {
		t.Fatal("nested assistant text terminated the task")
	}
	if text := w.lastAssistantText(); text != "" {
		t.Fatalf("nested assistant text became the task answer: %q", text)
	}
	nestedStream := externalclaude.Event{
		Type:            externalclaude.TypeStreamEvent,
		ParentToolUseID: "agent-call",
		StreamEvent: &externalclaude.StreamEvent{Type: "content_block_delta", Delta: &externalclaude.StreamDelta{
			Type: "text_delta", Text: "nested delta",
		}},
	}
	if terminal, _ := w.handleExternalEvent(nestedStream); terminal {
		t.Fatal("nested stream text terminated the task")
	}
	for _, msg := range w.messageSnapshot() {
		if strings.Contains(msg.Content, "nested") {
			t.Fatalf("nested text reached the task transcript: %q", msg.Content)
		}
	}
	topLevel := externalclaude.Event{
		Type: externalclaude.TypeAssistant,
		Message: &externalclaude.Message{Content: []externalclaude.ContentBlock{{
			Type: "text", Text: "top-level answer",
		}}},
	}
	if terminal, _ := w.handleExternalEvent(topLevel); terminal {
		t.Fatal("top-level assistant text terminated the task")
	}
	if text := w.lastAssistantText(); text != "top-level answer" {
		t.Fatalf("top-level answer = %q", text)
	}
}
