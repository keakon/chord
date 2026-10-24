package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// Context-only appends (TUI !shell output) must never change a task's
// lifecycle: reviving a terminal task to deliver a note would undo the
// completion or cancellation it already recorded, and leaking the note into
// the main session would put worker-scoped output in the wrong conversation.

func TestParkedCancelledTaskContextAppendKeepsTaskTerminal(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-ctx-cancelled")
	sub.setState(SubAgentStateCancelled, "stopped by user")
	a.syncTaskRecordFromSub(sub, "stopped by user")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want parked cancelled worker")
	}
	a.SwitchFocus(sub.instanceID)
	before := len(a.ctxMgr.Snapshot())

	a.AppendContextMessage(message.Message{Role: message.RoleUser, Content: "shell output for a cancelled task"})

	if got := a.subAgentByTaskID(sub.taskID); got != nil {
		t.Fatalf("cancelled task was rehydrated for a context append: %#v", got)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCancelled) {
		t.Fatalf("task record after context append = %#v, want cancelled", rec)
	}
	if got := len(a.ctxMgr.Snapshot()); got != before {
		t.Fatalf("main context rows = %d after refused append, want unchanged %d", got, before)
	}
}

func TestParkedCompletedTaskContextAppendKeepsTaskTerminal(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-ctx-completed")
	sub.setState(SubAgentStateCompleted, "finished")
	a.syncTaskRecordFromSub(sub, "finished")
	if !a.parkSubAgent(sub.instanceID) {
		t.Fatal("parkSubAgent() = false, want parked completed worker")
	}
	a.SwitchFocus(sub.instanceID)
	before := len(a.ctxMgr.Snapshot())

	a.AppendContextMessage(message.Message{Role: message.RoleUser, Content: "shell output for a finished task"})

	if got := a.subAgentByTaskID(sub.taskID); got != nil {
		t.Fatalf("completed task was rehydrated for a context append: %#v", got)
	}
	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.State != string(SubAgentStateCompleted) {
		t.Fatalf("task record after context append = %#v, want completed", rec)
	}
	if got := len(a.ctxMgr.Snapshot()); got != before {
		t.Fatalf("main context rows = %d after refused append, want unchanged %d", got, before)
	}
}

func TestExternalTaskContextAppendRefusedWithoutMainLeak(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	record := &DurableTaskRecord{
		TaskID:           "task-external-ctx",
		LatestInstanceID: "external-1",
		State:            string(SubAgentStateFailed),
		RuntimeParked:    true,
		ExternalSession:  &ExternalTaskSession{Backend: "claude-code", SessionID: "external-session"},
	}
	a.subs.mu.Lock()
	a.subs.taskRecords[record.TaskID] = record
	a.subs.mu.Unlock()
	a.setFocusedTaskID(record.TaskID)
	before := len(a.ctxMgr.Snapshot())

	a.AppendContextMessage(message.Message{Role: message.RoleUser, Content: "shell output for an external task"})

	if got := a.subs.runtimeByTaskID(record.TaskID); got != nil {
		t.Fatalf("external task was rehydrated for a context append: %#v", got)
	}
	if got := len(a.ctxMgr.Snapshot()); got != before {
		t.Fatalf("main context rows = %d after refused external append, want unchanged %d", got, before)
	}
	toast := waitForToastEvent(t, a.Events(), "external agent")
	if !strings.Contains(toast.Message, record.TaskID) {
		t.Fatalf("toast %q does not name the focused task %q", toast.Message, record.TaskID)
	}
}
