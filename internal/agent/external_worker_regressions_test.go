package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/externalclaude"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

func TestExternalWorkerPersistsRunningStateOnStart(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	script := writeFakeClaude(t, "exec sleep 60\n")
	configureExternalTestRuntime(t, a, script, `"*": allow`)
	handle, err := a.CreateSubAgent(context.Background(), tools.SubAgentRequest{
		AgentType: "external-worker", Description: "Wait.",
	})
	if err != nil {
		t.Fatal(err)
	}
	record := a.taskRecordByTaskID(handle.TaskID)
	if record == nil || record.State != string(SubAgentStateRunning) {
		t.Fatalf("started external task record = %+v, want running", record)
	}
}

func TestNormalizeRestoredExternalTaskRecords(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"running": {
			TaskID: "running", Attempt: 1, State: string(SubAgentStateRunning),
			LatestInstanceID: "external-1", RuntimeParked: false,
			ExternalSession: &ExternalTaskSession{Backend: config.BackendKindClaudeCode},
		},
		"ended": {
			TaskID: "ended", Attempt: 1, State: string(SubAgentStateWaitingMain),
			LatestInstanceID: "external-2", RuntimeParked: false,
			ExternalSession: &ExternalTaskSession{Backend: config.BackendKindClaudeCode, ProcessEnded: true},
		},
	})
	a.normalizeRestoredExternalTaskRecords()
	for _, taskID := range []string{"running", "ended"} {
		record := a.taskRecordByTaskID(taskID)
		if record == nil || record.State != string(SubAgentStateFailed) || !record.RuntimeParked {
			t.Fatalf("restored external task %s = %+v, want failed and parked", taskID, record)
		}
		if record.ClosedReason != externalTaskInterruptedReason {
			t.Fatalf("restored external task %s reason = %q", taskID, record.ClosedReason)
		}
	}
	if record := a.taskRecordByTaskID("running"); record.ExternalSession.ProcessEnded {
		t.Fatal("a task without a cleanup receipt was restored as ended")
	}
	if record := a.taskRecordByTaskID("ended"); !record.ExternalSession.ProcessEnded {
		t.Fatal("a task with recorded process end lost that state during restore")
	}
}

func TestSettlementPreservesExternalCompletionEnvelope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	completion := &CompletionEnvelope{
		Summary: "external done", ResultType: "text", Result: json.RawMessage(`"ok"`),
	}
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-1": {
			TaskID: "task-1", Attempt: 1, State: string(SubAgentStateCompleted),
			LatestInstanceID: "external-1", LastCompletion: completion,
			ExternalSession: &ExternalTaskSession{Backend: config.BackendKindClaudeCode, ProcessEnded: true},
		},
	})
	if got := a.settleDetachedTerminalTask("task-1", SubAgentStateCompleted, "external done", ""); got != SubAgentStateCompleted {
		t.Fatalf("settle state = %q, want completed", got)
	}
	record := a.taskRecordByTaskID("task-1")
	if record == nil || record.LastCompletion == nil {
		t.Fatalf("settled record = %+v, want the completion envelope", record)
	}
	if record.LastCompletion.Summary != "external done" || record.LastCompletion.ResultType != "text" {
		t.Fatalf("completion = %+v, want the external envelope", record.LastCompletion)
	}
}

func TestCompactionExcludesSelfManagedExternalRuntime(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	newTestExternalRuntime(t, a, "task-external-compaction")
	a.subs.mu.Lock()
	a.subs.taskRecords["task-external-compaction-done"] = &DurableTaskRecord{
		TaskID: "task-external-compaction-done", Attempt: 1, State: string(SubAgentStateCompleted),
		LatestInstanceID: "external-done",
		ExternalSession:  &ExternalTaskSession{Backend: config.BackendKindClaudeCode, ProcessEnded: true},
	}
	a.subs.mu.Unlock()
	if infos := a.taskInfosForCompaction(); len(infos) != 0 {
		t.Fatalf("compaction task infos = %+v; external agents manage their own context", infos)
	}
}

func TestSyncSubAgentOverlayRefreshesExternalWorkerRuleset(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", "Write: deny\n")
	w := newTestExternalRuntime(t, a, "task-external-rules")
	w.agentDefName = "external-worker"
	w.setRuleset(a.buildSubAgentRuleset(a.agentConfigs["external-worker"]))
	if got := w.currentRuleset().Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("initial external worker Write permission = %q, want deny", got)
	}

	if err := a.AddOverlayRule(permission.Rule{Permission: tools.NameShell, Pattern: "*", Action: permission.ActionAllow}, permission.ScopeSession); err != nil {
		t.Fatalf("AddOverlayRule: %v", err)
	}
	if got := w.currentRuleset().Evaluate(tools.NameShell, "git status --short"); got != permission.ActionAllow {
		t.Fatalf("external worker overlay Shell permission = %q, want allow", got)
	}
	if got := w.currentRuleset().Evaluate(tools.NameWrite, "notes.txt"); got != permission.ActionDeny {
		t.Fatalf("external worker Write permission after overlay sync = %q, want deny", got)
	}
}

func TestExternalFirstCleanupDoesNotBlockEventLoop(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	w := newTestExternalRuntime(t, a, "task-external-async-cleanup")
	a.subs.mu.Lock()
	a.subs.taskRecords[w.taskID].ExternalSession = &ExternalTaskSession{Backend: config.BackendKindClaudeCode}
	a.subs.mu.Unlock()
	w.done = make(chan struct{})
	w.started.Store(true)
	w.doneOnce.Do(func() { close(w.done) })
	w.promptMu.Lock()
	w.processEnded = false
	w.promptMu.Unlock()
	w.sessionMu.Lock()
	w.session = &externalclaude.Session{}
	w.sessionMu.Unlock()
	t.Cleanup(func() {
		w.sessionMu.Lock()
		w.session = nil
		w.sessionMu.Unlock()
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	previous := forceExternalProcessCleanup
	forceExternalProcessCleanup = func(*externalclaude.Session) bool {
		close(entered)
		<-release
		return true
	}
	t.Cleanup(func() { forceExternalProcessCleanup = previous })
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	closed := make(chan struct{})
	go func() {
		a.closeExternalWorker(w, SubAgentStateFailed, "cleanup blocked", "cleanup blocked", nil, 0, false)
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("closeExternalWorker blocked on the first cleanup attempt")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first cleanup attempt did not start")
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.promptMu.Lock()
		ended := w.processEnded
		w.promptMu.Unlock()
		if ended {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("asynchronous cleanup did not record process termination")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestExternalStreamIgnoresCitationDelta(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	w := &externalWorker{instanceID: "external-1", parent: a}
	event := externalclaude.Event{StreamEvent: &externalclaude.StreamEvent{
		Type: "content_block_delta", Delta: &externalclaude.StreamDelta{Type: "citations_delta"},
	}}
	if w.handleExternalStreamEvent(event) {
		t.Fatal("a citations delta terminated the external task")
	}
	if err := w.getResultError(); err != nil {
		t.Fatalf("citations delta set a result error: %v", err)
	}
}

func TestExternalPermissionReplayRequiresSameToolCall(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: ctx, cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.setState(SubAgentStateRunning, "running")
	first := externalclaude.Event{RequestID: "req-1", Request: &externalclaude.ControlRequest{
		ToolName: "Read", ToolUseID: "tool-1", Input: json.RawMessage(`{"file_path":"sample.txt"}`),
	}}
	w.beginExternalPermission(first)
	w.permissionWG.Wait()
	w.permissionMu.Lock()
	outcome := w.permissionOutcomes["req-1"]
	if outcome == nil || outcome.toolUseID != "tool-1" {
		w.permissionMu.Unlock()
		t.Fatalf("permission outcome = %+v, want tool-1", outcome)
	}
	w.permissionMu.Unlock()

	second := first
	second.Request = &externalclaude.ControlRequest{
		ToolName: "Read", ToolUseID: "tool-1", Input: json.RawMessage(`{"file_path":"other.txt"}`),
	}
	w.beginExternalPermission(second)
	w.permissionWG.Wait()
	w.permissionMu.Lock()
	defer w.permissionMu.Unlock()
	if got := w.permissionOutcomes["req-1"]; got != outcome || got.toolUseID != "tool-1" {
		t.Fatalf("reused request id replaced the cached outcome: %+v", got)
	}
	if len(w.permissionOutcomes) != 1 {
		t.Fatalf("permission outcomes = %d, want 1", len(w.permissionOutcomes))
	}
}

func TestExternalPermissionEmptyRequestIDIsNotCached(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	configureExternalTestRuntime(t, a, "claude", `"*": allow`)
	w, err := a.newExternalWorker(externalWorkerConfig{
		agentDef: a.agentConfigs["external-worker"], instanceID: "external-1", taskID: "task-1",
		ctx: context.Background(), cancel: func() {},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.setState(SubAgentStateRunning, "running")
	w.beginExternalPermission(externalclaude.Event{Request: &externalclaude.ControlRequest{
		ToolName: "Read", ToolUseID: "tool-1", Input: json.RawMessage(`{"file_path":"sample.txt"}`),
	}})
	w.permissionWG.Wait()
	w.permissionMu.Lock()
	defer w.permissionMu.Unlock()
	if len(w.permissionOutcomes) != 0 {
		t.Fatalf("empty request id was cached: %+v", w.permissionOutcomes)
	}
}
