package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func toolMessagesForCall(t *testing.T, msgs []message.Message, callID string) []message.Message {
	t.Helper()
	var out []message.Message
	for _, msg := range msgs {
		if msg.Role == "tool" && msg.ToolCallID == callID {
			out = append(out, msg)
		}
	}
	return out
}

// An admitted call without a trusted result may already have produced external
// side effects, so cancellation must settle it as outcome_unknown instead of
// claiming it never ran.
func TestMainTurnCancelSettlesAdmittedCallAsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	turnID := a.turn.ID
	call := message.ToolCall{ID: "mcp-admitted", Name: "mcp_srv_tool", Args: []byte(`{"q":"x"}`)}
	a.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{call}})
	a.turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})
	if !a.turn.admitToolExecution(call.ID) {
		t.Fatal("admitToolExecution() = false before cancellation")
	}

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: turnID,
		Payload: &TurnCancelledPayload{
			TurnID:              turnID,
			Calls:               []PendingToolCall{{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)}},
			MarkToolCallsFailed: true,
		},
	})
	a.flushPersist()

	results := toolMessagesForCall(t, a.GetMessages(), call.ID)
	if len(results) != 1 {
		t.Fatalf("tool messages for cancelled call = %d, want 1", len(results))
	}
	if results[0].ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("recovery state = %q, want %q", results[0].ToolRecoveryState, message.ToolRecoveryStateOutcomeUnknown)
	}
	if results[0].Content != interruptedToolOutcomeUnknownText {
		t.Fatalf("terminal text = %q, want interrupted outcome-unknown text", results[0].Content)
	}
	if strings.Contains(strings.ToLower(results[0].Content), "safe to retry") {
		t.Fatalf("outcome-unknown text must not invite a blind retry: %q", results[0].Content)
	}
}

// A call that never passed admission is proven not to have executed, so
// cancellation keeps the ordinary cancelled terminal.
func TestMainTurnCancelNeverAdmittedStaysCancelled(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	turnID := a.turn.ID
	call := message.ToolCall{ID: "never-admitted", Name: "read", Args: []byte(`{"path":"README.md"}`)}
	a.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{call}})
	a.turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: turnID,
		Payload: &TurnCancelledPayload{
			TurnID: turnID,
			Calls:  []PendingToolCall{{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)}},
		},
	})
	a.flushPersist()

	results := toolMessagesForCall(t, a.GetMessages(), call.ID)
	if len(results) != 1 {
		t.Fatalf("tool messages for cancelled call = %d, want 1", len(results))
	}
	if results[0].ToolStatus != string(message.ToolStatusCancelled) || results[0].Content != "Cancelled" {
		t.Fatalf("tool result = %#v, want ordinary cancellation", results[0])
	}
	if results[0].ToolRecoveryState != "" {
		t.Fatalf("recovery state = %q, want empty for a proven not-started call", results[0].ToolRecoveryState)
	}
}

// A result that finished after the cancellation decision but before the loop
// closeout outranks the cancellation: the call completed, so its real output is
// persisted and a later duplicate event is dropped.
func TestMainTurnCancelPreservesResultRegisteredAfterCancelDecision(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn("")
	turnID := a.turn.ID
	call := message.ToolCall{ID: "completed-late", Name: "read", Args: []byte(`{"path":"README.md"}`)}
	a.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{call}})
	a.turn.recordPendingToolCall(PendingToolCall{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)})
	if !a.turn.admitToolExecution(call.ID) {
		t.Fatal("admitToolExecution() = false before cancellation")
	}

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}
	payload := &ToolResultPayload{
		CallID:   call.ID,
		Name:     call.Name,
		ArgsJSON: string(call.Args),
		Result:   "file contents",
		TurnID:   turnID,
	}
	if !a.turn.registerReadyToolResult(call.ID, payload) {
		t.Fatal("registerReadyToolResult() = false after cancel decision, want true before closeout")
	}
	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: turnID,
		Payload: &TurnCancelledPayload{
			TurnID:              turnID,
			Calls:               []PendingToolCall{{CallID: call.ID, Name: call.Name, ArgsJSON: string(call.Args)}},
			MarkToolCallsFailed: true,
		},
	})
	a.flushPersist()

	results := toolMessagesForCall(t, a.GetMessages(), call.ID)
	if len(results) != 1 {
		t.Fatalf("tool messages for cancelled call = %d, want 1", len(results))
	}
	if results[0].ToolStatus != message.ToolStatusSuccess || !strings.Contains(results[0].Content, "file contents") {
		t.Fatalf("tool result = %#v, want preserved completed result", results[0])
	}

	// The execution goroutine's own event arrives after the closeout consumed
	// the result; it must not append a second terminal.
	a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: payload})
	if got := len(toolMessagesForCall(t, a.GetMessages(), call.ID)); got != 1 {
		t.Fatalf("tool messages after late duplicate = %d, want 1", got)
	}
}

func TestToolSettlementRefusesLateResultAfterCloseout(t *testing.T) {
	t.Parallel()
	turn := &Turn{}
	if !turn.admitToolExecution("call-1") {
		t.Fatal("admitToolExecution() = false before closeout")
	}
	turn.closeToolAdmissions()
	if turn.admitToolExecution("call-2") {
		t.Fatal("admitToolExecution() = true after admissions closed, want false")
	}
	evidence := turn.takeToolSettlement()
	if !evidence.wasAdmitted("call-1") {
		t.Fatal("settlement evidence lost the admitted call")
	}
	calls := []PendingToolCall{{CallID: "call-1"}}
	synthetic, preserved := closeInterruptedToolCalls(turn, evidence, calls, nil)
	if preserved != 0 || len(synthetic) != 1 {
		t.Fatalf("closeout = %d synthetic / %d preserved, want 1 / 0", len(synthetic), preserved)
	}
	if synthetic[0].TerminalRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("settled recovery state = %q, want %q", synthetic[0].TerminalRecoveryState, message.ToolRecoveryStateOutcomeUnknown)
	}
	// A result that finishes after the conservative settlement must not reopen
	// the call or downgrade its outcome.
	if turn.registerReadyToolResult("call-1", &ToolResultPayload{CallID: "call-1", Result: "late"}) {
		t.Fatal("registerReadyToolResult() = true after settlement, want false")
	}
	again, _ := closeInterruptedToolCalls(turn, evidence, calls, nil)
	if len(again) != 0 {
		t.Fatalf("second closeout synthesized %d terminals, want 0", len(again))
	}
}

func TestSubTurnInterruptSettlesAdmittedAndPreservesCompletedCalls(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "settlement")
	sub.newTurn()
	admitted := message.ToolCall{ID: "sub-admitted", Name: "mcp_srv_tool", Args: []byte(`{"q":"x"}`)}
	completed := message.ToolCall{ID: "sub-completed", Name: "read", Args: []byte(`{"path":"README.md"}`)}
	sub.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{admitted, completed}})
	sub.turn.recordPendingToolCall(PendingToolCall{CallID: admitted.ID, Name: admitted.Name, ArgsJSON: string(admitted.Args), AgentID: sub.instanceID})
	sub.turn.recordPendingToolCall(PendingToolCall{CallID: completed.ID, Name: completed.Name, ArgsJSON: string(completed.Args), AgentID: sub.instanceID})
	if !sub.turn.admitToolExecution(admitted.ID) {
		t.Fatal("admitToolExecution() = false before interrupt")
	}
	if !sub.turn.admitToolExecution(completed.ID) {
		t.Fatal("admitToolExecution() = false before interrupt")
	}
	sub.turn.registerReadyToolResult(completed.ID, &toolResult{
		CallID:   completed.ID,
		Name:     completed.Name,
		ArgsJSON: string(completed.Args),
		Result:   "done",
		TurnID:   sub.turn.ID,
	})

	if !sub.interruptCurrentTurnWithStatus(message.ToolStatusError, context.Canceled, false) {
		t.Fatal("interruptCurrentTurnWithStatus() = false, want true")
	}
	a.flushPersist()

	admittedResults := toolMessagesForCall(t, sub.GetMessages(), admitted.ID)
	if len(admittedResults) != 1 {
		t.Fatalf("tool messages for admitted call = %d, want 1", len(admittedResults))
	}
	if admittedResults[0].ToolRecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("admitted call recovery state = %q, want %q", admittedResults[0].ToolRecoveryState, message.ToolRecoveryStateOutcomeUnknown)
	}
	completedResults := toolMessagesForCall(t, sub.GetMessages(), completed.ID)
	if len(completedResults) != 1 {
		t.Fatalf("tool messages for completed call = %d, want 1", len(completedResults))
	}
	if completedResults[0].ToolStatus != message.ToolStatusSuccess || !strings.Contains(completedResults[0].Content, "done") {
		t.Fatalf("completed call result = %#v, want preserved success", completedResults[0])
	}

	// A late result for the settled admitted call must not add a second terminal.
	sub.handleToolResult(&toolResult{CallID: admitted.ID, Name: admitted.Name, ArgsJSON: string(admitted.Args), Result: "late", TurnID: sub.turn.ID})
	if got := len(toolMessagesForCall(t, sub.GetMessages(), admitted.ID)); got != 1 {
		t.Fatalf("tool messages after late sub result = %d, want 1", got)
	}
}

// A Speculative SubAgent execution that passed admission before the intent
// barrier failed must settle as outcome_unknown, and the barrier must not close
// admission for the whole turn.
func TestSubFailIntentBarrierSettlesAdmittedCallAsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "barrier")
	sub.newTurn()
	call := message.ToolCall{ID: "sub-barrier-admitted", Name: "mcp_srv_tool", Args: []byte(`{"q":"x"}`)}
	sub.ctxMgr.Append(message.Message{Role: "assistant", ToolCalls: []message.ToolCall{call}})
	if !sub.turn.admitToolExecution(call.ID) {
		t.Fatal("admitToolExecution() = false before the barrier")
	}

	sub.failIntentBarrier([]message.ToolCall{call}, context.Canceled)

	if len(sub.promotedToolQueue) != 1 {
		t.Fatalf("promoted results = %d, want 1", len(sub.promotedToolQueue))
	}
	payload := sub.promotedToolQueue[0]
	if payload.RecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		t.Fatalf("recovery state = %q, want %q", payload.RecoveryState, message.ToolRecoveryStateOutcomeUnknown)
	}
	if !strings.Contains(payload.Result, interruptedToolOutcomeUnknownText) {
		t.Fatalf("result = %q, want the outcome-unknown note", payload.Result)
	}
	if sub.turn.admitToolExecution(call.ID) {
		t.Fatal("abandoned sub call admitted execution after the barrier failure")
	}
	if !sub.turn.admitToolExecution("sub-next-round") {
		t.Fatal("turn-wide admission closed by a per-call barrier failure")
	}
}
