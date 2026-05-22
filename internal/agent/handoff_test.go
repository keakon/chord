package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestHandoffToolResultLeavesAgentIdleForUserSelection(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn()
	turnID := a.turn.ID
	callID := "handoff-1"
	a.ctxMgr.Append(message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   callID,
			Name: tools.NameHandoff,
			Args: []byte(`{"plan_path":"docs/plans/example.md"}`),
		}},
	})
	a.turn.PendingToolCalls.Store(1)
	a.turn.TotalToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: callID, Name: tools.NameHandoff, ArgsJSON: `{"plan_path":"docs/plans/example.md"}`})

	a.handleToolResult(Event{Type: EventToolResult, TurnID: turnID, Payload: &ToolResultPayload{
		CallID:   callID,
		Name:     tools.NameHandoff,
		ArgsJSON: `{"plan_path":"docs/plans/example.md"}`,
		Result:   `{"plan_path":"docs/plans/example.md"}`,
		TurnID:   turnID,
	}})

	if a.turn != nil {
		t.Fatalf("turn after Handoff result = %#v, want nil while waiting for user selection", a.turn)
	}
	if a.lastPlanPath != "docs/plans/example.md" {
		t.Fatalf("lastPlanPath = %q, want plan path", a.lastPlanPath)
	}
}
