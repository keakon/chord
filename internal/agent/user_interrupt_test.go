package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestCancelCurrentTurnKeepsPendingInputsQueuedAndFailsToolCalls(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}

	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-user-cancel",
			Name: "web_fetch",
			Args: []byte(`{"url":"https://slow.example"}`),
		}},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()
	a.turn.PendingToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{
		CallID:   "tool-user-cancel",
		Name:     "web_fetch",
		ArgsJSON: `{"url":"https://slow.example"}`,
	})
	a.pendingUserMessages = []pendingUserMessage{{
		DraftID:  "draft-1",
		Content:  "queued after cancel",
		FromUser: true,
	}}

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	select {
	case evt := <-a.eventCh:
		payload, ok := evt.Payload.(*TurnCancelledPayload)
		if !ok {
			t.Fatalf("payload type = %T, want *TurnCancelledPayload", evt.Payload)
		}
		if !payload.MarkToolCallsFailed {
			t.Fatal("MarkToolCallsFailed = false, want true")
		}
		if !payload.KeepPendingUserMessagesQueued {
			t.Fatal("KeepPendingUserMessagesQueued = false, want true")
		}
		if !payload.CommitPendingUserMessagesWithoutTurn {
			t.Fatal("CommitPendingUserMessagesWithoutTurn = false, want true")
		}

		a.handleTurnCancelled(evt)
	default:
		t.Fatal("expected turn-cancelled event")
	}

	a.flushPersist()

	if a.turn != nil {
		t.Fatal("expected turn to be cleared after cancellation")
	}
	if got := len(a.pendingUserMessages); got != 0 {
		t.Fatalf("len(pendingUserMessages) = %d, want 0 after committing queued user input", got)
	}

	msgs := a.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("len(GetMessages()) = %d, want 3", len(msgs))
	}
	if msgs[1].Role != "tool" {
		t.Fatalf("second message role = %q, want tool", msgs[1].Role)
	}
	if !strings.Contains(msgs[1].Content, "context canceled") {
		t.Fatalf("second message content = %q, want failure message with context canceled", msgs[1].Content)
	}
	if msgs[2].Role != "user" {
		t.Fatalf("third message role = %q, want user", msgs[2].Role)
	}
	if msgs[2].Content != "queued after cancel" {
		t.Fatalf("third message content = %q, want queued user input committed", msgs[2].Content)
	}
}

func TestCancelCurrentTurnInterruptsRunningSubAgents(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)
	sub := newPersistenceTestSubAgent(a, "agent-1")

	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-sub-interrupt",
			Name: "web_fetch",
			Args: []byte(`{"url":"https://slow.example"}`),
		}},
	}
	sub.ctxMgr.Append(assistant)
	if err := a.recoveryManager().PersistMessage(sub.instanceID, assistant); err != nil {
		t.Fatalf("PersistMessage(sub assistant): %v", err)
	}
	sub.turn.PendingToolCalls.Store(1)
	sub.turn.recordPendingToolCall(PendingToolCall{
		CallID:   "tool-sub-interrupt",
		Name:     "web_fetch",
		ArgsJSON: `{"url":"https://slow.example"}`,
		AgentID:  sub.instanceID,
	})
	a.subs.mu.Lock()
	a.subs.subAgents[sub.instanceID] = sub
	a.subs.mu.Unlock()

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	a.flushPersist()

	msgs := sub.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("len(sub.GetMessages()) = %d, want 2", len(msgs))
	}
	if msgs[1].Role != "tool" {
		t.Fatalf("sub tool message role = %q, want tool", msgs[1].Role)
	}
	if got := msgs[1].Content; got != toolCallFailureMessage(context.Canceled) {
		t.Fatalf("sub tool message = %q, want %q", got, toolCallFailureMessage(context.Canceled))
	}

	restored, err := a.recoveryManager().LoadMessages(sub.instanceID)
	if err != nil {
		t.Fatalf("LoadMessages(sub): %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("len(restored sub messages) = %d, want 2", len(restored))
	}
	if got := restored[1].Content; got != toolCallFailureMessage(context.Canceled) {
		t.Fatalf("restored sub tool message = %q, want %q", got, toolCallFailureMessage(context.Canceled))
	}
}

func TestCancelCurrentTurnReportsIdleSubAgentCancellationAndSnapshotsState(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-7")
	sub.setState(SubAgentStateIdle, "restored idle worker")
	a.saveRecoverySnapshot()

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false after cancelling an idle SubAgent")
	}
	if got := sub.State(); got != SubAgentStateCancelled {
		t.Fatalf("sub.State() = %q, want %q", got, SubAgentStateCancelled)
	}

	snapshot, err := a.recoveryManager().Recover()
	if err != nil {
		t.Fatalf("Recover(): %v", err)
	}
	if snapshot == nil {
		t.Fatal("expected recovery snapshot")
	}
	for _, restored := range snapshot.ActiveAgents {
		if restored.InstanceID == sub.instanceID {
			if restored.State != string(SubAgentStateCancelled) {
				t.Fatalf("snapshot state = %q, want %q", restored.State, SubAgentStateCancelled)
			}
			return
		}
	}
	t.Fatalf("snapshot missing SubAgent %q: %#v", sub.instanceID, snapshot.ActiveAgents)
}

// parkedWaitingDescendantSettlement reads a task's durable settlement the way
// collect and retention do: a terminal record without a settlement still
// blocks waiters, so the stranded-owner tests assert on the journal, not just
// on the record state.
func parkedWaitingDescendantSettlement(t *testing.T, a *MainAgent, taskID string) *TaskSettlement {
	t.Helper()
	loaded, err := loadTaskSettlements(a.sessionDir)
	if err != nil {
		t.Fatalf("loadTaskSettlements: %v", err)
	}
	return loaded[taskAttemptKey{TaskID: taskID, Attempt: 1}]
}

// TestUserCancelSettlesParkedWaitingDescendantOwner pins the parked-owner
// convergence fix on the user-cancel path.
//
// An owner that delegated with child_join parks the instant it enters
// waiting_descendant (see enterWaitingDescendant), so it has no runtime and is
// invisible to interruptSubAgentTurnsForUserCancel's walk over
// a.subs.subAgents. A user cancel also settles the child straight through
// commitTerminalTask without emitting a mailbox, so the descendant-mailbox
// wake that resolves a normal child completion never fires either. Before the
// fix the owner stayed non-terminal for the rest of the live session:
// task.collect(wait) blocked until timeout, retention could not archive the
// record, and only repairRestoredTaskTree — which runs at restore — converged
// it.
func TestUserCancelSettlesParkedWaitingDescendantOwner(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-owner": {
			TaskID:        "task-owner",
			TaskDesc:      "integrate the child result",
			AgentDefName:  "worker",
			Attempt:       1,
			State:         string(SubAgentStateWaitingDescendant),
			Depth:         1,
			RuntimeParked: true,
			LastSummary:   "waiting for the joined child",
		},
	})

	child := newControllableTestSubAgent(t, a, "task-child")
	child.setState(SubAgentStateRunning, "working on the child task")
	a.subs.mu.Lock()
	child.ownerTaskID = "task-owner"
	child.depth = 2
	child.joinToOwner = true
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	childRecord := a.taskRecordByTaskID("task-child")
	if childRecord == nil || childRecord.OwnerTaskID != "task-owner" || !childRecord.JoinToOwner {
		t.Fatalf("child task record = %#v, want a joined child of task-owner", childRecord)
	}
	if live := a.subAgentByTaskID("task-owner"); live != nil {
		t.Fatal("owner must have no live runtime while parked in waiting_descendant")
	}

	if !a.interruptSubAgentTurnsForUserCancel() {
		t.Fatal("interruptSubAgentTurnsForUserCancel = false, want the child cancel to be reported")
	}

	childRecord = a.taskRecordByTaskID("task-child")
	if childRecord == nil || childRecord.State != string(SubAgentStateCancelled) {
		t.Fatalf("child state = %#v, want cancelled by the user interrupt", childRecord)
	}
	owner := a.taskRecordByTaskID("task-owner")
	if owner == nil || owner.State != string(SubAgentStateCancelled) {
		t.Fatalf("owner state = %#v, want the stranded waiting_descendant owner settled cancelled", owner)
	}
	if !owner.RuntimeParked {
		t.Fatalf("owner RuntimeParked = %v, want the settled parked record to stay parked", owner.RuntimeParked)
	}
	settlement := parkedWaitingDescendantSettlement(t, a, "task-owner")
	if settlement == nil || settlement.Outcome != string(SubAgentStateCancelled) {
		t.Fatalf("owner settlement = %#v, want a durable cancelled settlement", settlement)
	}
}

// TestUserCancelKeepsWaitingDescendantOwnerWithOutstandingChild guards the
// inverse: a parked waiting_descendant owner that still waits on another
// non-terminal joined child keeps waiting. The cancellation did not resolve
// its wait, so settling it would abandon work the user never asked to stop.
func TestUserCancelKeepsWaitingDescendantOwnerWithOutstandingChild(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-owner": {
			TaskID:        "task-owner",
			TaskDesc:      "integrate both child results",
			AgentDefName:  "worker",
			Attempt:       1,
			State:         string(SubAgentStateWaitingDescendant),
			Depth:         1,
			RuntimeParked: true,
		},
		// A sibling parked waiting on the main agent: the user cancel cannot
		// reach it either, so the owner's wait is still unresolved.
		"task-sibling": {
			TaskID:        "task-sibling",
			TaskDesc:      "wait for a decision",
			AgentDefName:  "worker",
			Attempt:       1,
			State:         string(SubAgentStateWaitingMain),
			OwnerTaskID:   "task-owner",
			Depth:         2,
			JoinToOwner:   true,
			RuntimeParked: true,
		},
	})

	child := newControllableTestSubAgent(t, a, "task-child")
	child.setState(SubAgentStateRunning, "working on the child task")
	a.subs.mu.Lock()
	child.ownerTaskID = "task-owner"
	child.depth = 2
	child.joinToOwner = true
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(child, "")

	if !a.interruptSubAgentTurnsForUserCancel() {
		t.Fatal("interruptSubAgentTurnsForUserCancel = false, want the child cancel to be reported")
	}
	if childRecord := a.taskRecordByTaskID("task-child"); childRecord == nil || childRecord.State != string(SubAgentStateCancelled) {
		t.Fatalf("child state = %#v, want cancelled by the user interrupt", childRecord)
	}
	owner := a.taskRecordByTaskID("task-owner")
	if owner == nil || owner.State != string(SubAgentStateWaitingDescendant) {
		t.Fatalf("owner state = %#v, want it to keep waiting on the outstanding sibling", owner)
	}
	if settlement := parkedWaitingDescendantSettlement(t, a, "task-owner"); settlement != nil {
		t.Fatalf("owner settlement = %#v, want no settlement for an unresolved wait", settlement)
	}
}

// TestUserCancelSettlesParkedAncestorChain pins the upward walk: a cancelled
// joined child can strand a whole chain of parked waiting_descendant
// ancestors, and each level's wait is only resolved once its own joined
// children are terminal.
func TestUserCancelSettlesParkedAncestorChain(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.setTaskRecords(map[string]*DurableTaskRecord{
		"task-root": {
			TaskID:        "task-root",
			TaskDesc:      "root integration",
			AgentDefName:  "worker",
			Attempt:       1,
			State:         string(SubAgentStateWaitingDescendant),
			Depth:         1,
			RuntimeParked: true,
		},
		"task-mid": {
			TaskID:        "task-mid",
			TaskDesc:      "mid integration",
			AgentDefName:  "worker",
			Attempt:       1,
			State:         string(SubAgentStateWaitingDescendant),
			OwnerTaskID:   "task-root",
			Depth:         2,
			JoinToOwner:   true,
			RuntimeParked: true,
		},
	})

	leaf := newControllableTestSubAgent(t, a, "task-leaf")
	leaf.setState(SubAgentStateRunning, "working on the leaf task")
	a.subs.mu.Lock()
	leaf.ownerTaskID = "task-mid"
	leaf.depth = 3
	leaf.joinToOwner = true
	a.subs.mu.Unlock()
	a.syncTaskRecordFromSub(leaf, "")

	if !a.interruptSubAgentTurnsForUserCancel() {
		t.Fatal("interruptSubAgentTurnsForUserCancel = false, want the leaf cancel to be reported")
	}
	for _, taskID := range []string{"task-leaf", "task-mid", "task-root"} {
		rec := a.taskRecordByTaskID(taskID)
		if rec == nil || rec.State != string(SubAgentStateCancelled) {
			t.Fatalf("%s state = %#v, want cancelled up the ancestor chain", taskID, rec)
		}
		settlement := parkedWaitingDescendantSettlement(t, a, taskID)
		if settlement == nil || settlement.Outcome != string(SubAgentStateCancelled) {
			t.Fatalf("%s settlement = %#v, want a durable cancelled settlement", taskID, settlement)
		}
	}
}
