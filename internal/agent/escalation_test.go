package agent

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// drainLoopEvents returns every event currently queued on the parent loop
// channel without waiting.
func drainLoopEvents(a *MainAgent) []Event {
	var events []Event
	for {
		select {
		case evt := <-a.eventCh:
			events = append(events, evt)
		default:
			return events
		}
	}
}

func lastToolMessage(t *testing.T, sub *SubAgent) message.Message {
	t.Helper()
	msgs := sub.ctxMgr.Snapshot()
	for _, msg := range slices.Backward(msgs) {
		if msg.Role == "tool" {
			return msg
		}
	}
	t.Fatal("no tool message in transcript")
	return message.Message{}
}

func escalateToolCall(t *testing.T, id string, args map[string]any) messageToolCall {
	t.Helper()
	return mustJSONToolCall(t, id, "escalate", args)
}

func TestSubAgentRejectsInvalidEscalateArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "missing kind", args: map[string]any{"reason": "need help"}},
		{name: "unknown kind", args: map[string]any{"kind": "maybe", "reason": "need help"}},
		{name: "missing reason", args: map[string]any{"kind": "needs_repair"}},
		{name: "blank reason", args: map[string]any{"kind": "needs_repair", "reason": "   "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, sub := newMixedBatchTestSubAgent(t)
			sub.handleLLMResponse(&llmResult{
				turnID: 1,
				resp:   &message.Response{ToolCalls: convertCalls([]messageToolCall{escalateToolCall(t, "call-1", tc.args)})},
			})

			toolMsg := lastToolMessage(t, sub)
			if toolMsg.ToolStatus != string(ToolResultStatusError) {
				t.Fatalf("tool status = %q, want error", toolMsg.ToolStatus)
			}
			if !strings.Contains(toolMsg.Content, "Escalation rejected") {
				t.Fatalf("tool result = %q, want a rejection", toolMsg.Content)
			}
			events := drainLoopEvents(parent)
			if len(events) != 1 || events[0].Type != EventAgentError {
				t.Fatalf("events = %#v, want one agent error", events)
			}
			if _, ok := events[0].Payload.(error); !ok {
				t.Fatalf("payload = %#v, want an error", events[0].Payload)
			}
		})
	}
}

func TestSubAgentNeedsRepairEscalateWaitsForOwner(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			escalateToolCall(t, "call-1", map[string]any{"kind": "needs_repair", "reason": "need a decision"}),
		})},
	})

	if got := lastToolMessage(t, sub).ToolStatus; got != string(ToolResultStatusSuccess) {
		t.Fatalf("tool status = %q, want success", got)
	}
	events := drainLoopEvents(parent)
	if len(events) != 1 || events[0].Type != EventEscalate {
		t.Fatalf("events = %#v, want one escalate", events)
	}
	payload, ok := events[0].Payload.(tools.AgentRequestPayload)
	if !ok || payload.Kind != tools.EscalateKindNeedsRepair || payload.Reason != "need a decision" {
		t.Fatalf("payload = %#v, want a needs_repair escalation", events[0].Payload)
	}
	if isTerminalSubAgentState(sub.State()) {
		t.Fatalf("state = %q, want a non-terminal worker", sub.State())
	}
}

func TestSubAgentBlockedEscalateClosesAsBlocked(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			escalateToolCall(t, "call-1", map[string]any{"kind": "blocked", "reason": "the upstream API is gone"}),
		})},
	})

	if got := lastToolMessage(t, sub).ToolStatus; got != string(ToolResultStatusError) {
		t.Fatalf("tool status = %q, want error", got)
	}
	events := drainLoopEvents(parent)
	if len(events) != 1 || events[0].Type != EventAgentError {
		t.Fatalf("events = %#v, want one agent error", events)
	}
	cause, ok := events[0].Payload.(error)
	if !ok {
		t.Fatalf("payload = %#v, want an error", events[0].Payload)
	}
	blocked, ok := errors.AsType[*blockedEscalationError](cause)
	if !ok {
		t.Fatalf("payload = %#v, want a blockedEscalationError", events[0].Payload)
	}
	if !strings.Contains(blocked.Error(), "the upstream API is gone") {
		t.Fatalf("blocked error = %q, want the declared reason", blocked.Error())
	}
	if got := classifyAgentError(blocked); got != agentErrorKindBlocked {
		t.Fatalf("classifyAgentError = %q, want %q", got, agentErrorKindBlocked)
	}
}

func TestSubAgentRefusesEscalateAfterUnansweredBudget(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.subs.mu.Lock()
	parent.subs.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID:              sub.taskID,
		LatestInstanceID:    sub.instanceID,
		State:               string(SubAgentStateRunning),
		EscalationCount:     maxUnansweredSubAgentEscalations,
		EscalationMailboxID: "mailbox-1",
	}
	parent.subs.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			escalateToolCall(t, "call-1", map[string]any{"kind": "needs_repair", "reason": "still blocked"}),
		})},
	})

	for _, evt := range drainLoopEvents(parent) {
		if evt.Type == EventEscalate {
			t.Fatalf("escalate must be refused, got %#v", evt)
		}
	}
	if got := lastToolMessage(t, sub).ToolStatus; got != string(ToolResultStatusError) {
		t.Fatalf("tool status = %q, want error", got)
	}
	if sub.State() == SubAgentStateWaitingMain || isTerminalSubAgentState(sub.State()) {
		t.Fatalf("state = %q, want the worker to keep running", sub.State())
	}
	noticed := false
	for _, msg := range sub.ctxMgr.Snapshot() {
		if msg.Role == "user" && strings.Contains(msg.Content, "Escalation rejected") {
			noticed = true
		}
	}
	if !noticed {
		t.Fatal("expected the budget-exhausted notice in the transcript")
	}
}

func TestSubAgentEscalateAllowedBelowBudget(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.subs.mu.Lock()
	parent.subs.taskRecords[sub.taskID] = &DurableTaskRecord{
		TaskID:          sub.taskID,
		EscalationCount: maxUnansweredSubAgentEscalations - 1,
	}
	parent.subs.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			escalateToolCall(t, "call-1", map[string]any{"kind": "needs_repair", "reason": "one more try"}),
		})},
	})

	events := drainLoopEvents(parent)
	if len(events) != 1 || events[0].Type != EventEscalate {
		t.Fatalf("events = %#v, want the escalation to be routed", events)
	}
}

func TestRecordSubAgentEscalationPersistsBudget(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-escalation-budget")

	a.recordSubAgentEscalation(sub, "mailbox-1")
	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.EscalationCount != 1 || rec.EscalationMailboxID != "mailbox-1" {
		t.Fatalf("record = %#v, want one escalation recorded at mailbox-1", rec)
	}
	if !a.subAgentEscalationAllowed(sub.taskID) {
		t.Fatal("the first unanswered escalation must be allowed")
	}

	a.recordSubAgentEscalation(sub, "mailbox-2")
	if a.subAgentEscalationAllowed(sub.taskID) {
		t.Fatal("the budget must be exhausted after the limit of unanswered escalations")
	}

	onDisk, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	rec := onDisk[sub.taskID]
	if rec == nil || rec.EscalationCount != maxUnansweredSubAgentEscalations || rec.EscalationMailboxID != "mailbox-2" {
		t.Fatalf("persisted record = %#v, want count %d at mailbox-2", rec, maxUnansweredSubAgentEscalations)
	}
}

func TestBuildTaskRecordFromSubResetsEscalationBudgetOnAnsweredEscalation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		replyToMailbox string
		wantCount      int
		wantMailboxID  string
	}{
		{name: "owner answered the escalation", replyToMailbox: "escalation-1", wantCount: 0, wantMailboxID: ""},
		{name: "owner initiated a delivery", replyToMailbox: "", wantCount: 2, wantMailboxID: "escalation-1"},
		{name: "owner answered another mailbox", replyToMailbox: "other-1", wantCount: 2, wantMailboxID: "escalation-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, sub := newMixedBatchTestSubAgent(t)
			sub.setReplyThread("reply-1", tc.replyToMailbox, "correction", "summary")
			previous := &DurableTaskRecord{
				TaskID:              sub.taskID,
				Attempt:             1,
				EscalationCount:     maxUnansweredSubAgentEscalations,
				EscalationMailboxID: "escalation-1",
			}

			rec := buildTaskRecordFromSub(sub, previous, "", 1, time.Now())
			if rec.EscalationCount != tc.wantCount || rec.EscalationMailboxID != tc.wantMailboxID {
				t.Fatalf("record count/mailbox = %d/%q, want %d/%q", rec.EscalationCount, rec.EscalationMailboxID, tc.wantCount, tc.wantMailboxID)
			}
		})
	}
}

func TestHandleEscalateRecordsUnansweredEscalation(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-escalate-record")

	a.handleEscalate(Event{SourceID: sub.instanceID, Payload: tools.AgentRequestPayload{Kind: tools.EscalateKindNeedsRepair, Reason: "need a decision"}})

	rec := a.taskRecordByTaskID(sub.taskID)
	if rec == nil || rec.EscalationCount != 1 || strings.TrimSpace(rec.EscalationMailboxID) == "" {
		t.Fatalf("record = %#v, want one unanswered escalation carrying its mailbox", rec)
	}
	if !a.subAgentEscalationAllowed(sub.taskID) {
		t.Fatal("one unanswered escalation must not exhaust the budget")
	}

	onDisk, err := loadDurableTaskRecords(a.sessionDir)
	if err != nil {
		t.Fatalf("loadDurableTaskRecords: %v", err)
	}
	if got := onDisk[sub.taskID]; got == nil || got.EscalationMailboxID != rec.EscalationMailboxID {
		t.Fatalf("persisted record = %#v, want mailbox %q", got, rec.EscalationMailboxID)
	}
}

func TestSubAgentDefersEscalateRefusalUntilRegularToolsComplete(t *testing.T) {
	parent, sub := newMixedBatchTestSubAgent(t)
	parent.subs.mu.Lock()
	parent.subs.taskRecords[sub.taskID] = &DurableTaskRecord{TaskID: sub.taskID, EscalationCount: maxUnansweredSubAgentEscalations}
	parent.subs.mu.Unlock()

	sub.handleLLMResponse(&llmResult{
		turnID: 1,
		resp: &message.Response{ToolCalls: convertCalls([]messageToolCall{
			escalateToolCall(t, "call-1", map[string]any{"kind": "needs_repair", "reason": "still blocked"}),
			mustJSONToolCall(t, "call-2", "Dummy", map[string]any{"value": "x"}),
		})},
	})
	if sub.pendingEscalateRefusal == nil {
		t.Fatal("expected the refusal to be deferred behind the regular tool")
	}
	if sub.pendingEscalateRequest != nil {
		t.Fatal("a refused escalation must not be queued for routing")
	}

	sub.handleToolResult(&toolResult{CallID: "call-2", Name: "Dummy", ArgsJSON: `{"value":"x"}`, Result: "ok", TurnID: 1})

	for _, evt := range drainLoopEvents(parent) {
		if evt.Type == EventEscalate {
			t.Fatalf("escalate must be refused, got %#v", evt)
		}
	}
	if sub.pendingEscalateRefusal != nil {
		t.Fatal("the deferred refusal should be consumed after the batch settles")
	}
	if sub.State() == SubAgentStateWaitingMain || isTerminalSubAgentState(sub.State()) {
		t.Fatalf("state = %q, want the worker to keep running", sub.State())
	}
	// Every call in the batch must still be answered so the transcript pairing
	// stays intact: the escalate call with the refusal, the sibling with its own
	// result.
	statuses := map[string]string{}
	for _, msg := range sub.ctxMgr.Snapshot() {
		if msg.Role == "tool" {
			statuses[msg.ToolCallID] = msg.ToolStatus
		}
	}
	if statuses["call-1"] != string(ToolResultStatusError) {
		t.Fatalf("escalate tool status = %q, want error", statuses["call-1"])
	}
	if _, ok := statuses["call-2"]; !ok {
		t.Fatalf("sibling tool call was left unanswered: %#v", statuses)
	}
}

func TestBlockedEscalationClosesTaskThroughFailurePath(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "adhoc-blocked-escalation")

	a.handleAgentError(Event{Type: EventAgentError, SourceID: sub.instanceID, Payload: newBlockedEscalationError("upstream API retired")})

	select {
	case evt := <-a.eventCh:
		if evt.Type != EventSubAgentMailbox {
			t.Fatalf("event = %#v, want the risk alert mailbox", evt)
		}
		msg, ok := evt.Payload.(*SubAgentMailboxMessage)
		if !ok || msg.Kind != SubAgentMailboxKindRiskAlert {
			t.Fatalf("mailbox = %#v, want a risk alert", evt.Payload)
		}
		if !strings.Contains(msg.Summary, agentErrorKindBlocked) {
			t.Fatalf("mailbox summary = %q, want the blocked category", msg.Summary)
		}
		a.dispatch(evt)
	case <-time.After(time.Second):
		t.Fatal("blocked escalation did not queue a risk alert")
	}

	if rec := a.taskRecordByTaskID(sub.taskID); rec == nil || rec.State != string(SubAgentStateFailed) {
		t.Fatalf("task record = %#v, want failed", rec)
	}
}
