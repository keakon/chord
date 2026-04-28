package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func drainAgentEvents(events <-chan AgentEvent) []AgentEvent {
	var out []AgentEvent
	for {
		select {
		case ev := <-events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func nextNonRequestCycleEvent(t *testing.T, events <-chan AgentEvent) AgentEvent {
	t.Helper()
	for {
		evt := nextAgentEvent(t, events)
		if _, ok := evt.(RequestCycleStartedEvent); ok {
			continue
		}
		return evt
	}
}

func TestCancelCurrentTurnWithoutPendingToolsEmitsIdle(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: turnID,
		Payload: &TurnCancelledPayload{
			TurnID: turnID,
		},
	})

	if a.turn != nil {
		t.Fatal("expected turn to be cleared after cancellation")
	}

	evt := nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(AgentActivityEvent); !ok {
		t.Fatalf("first event type = %T, want AgentActivityEvent", evt)
	}
	evt = nextNonRequestCycleEvent(t, a.Events())
	if _, ok := evt.(IdleEvent); !ok {
		t.Fatalf("second event type = %T, want IdleEvent", evt)
	}
}

func TestCancelCurrentTurnWithPendingToolsPersistsCancelledToolResult(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	assistant := message.Message{
		Role: "assistant",
		ToolCalls: []message.ToolCall{{
			ID:   "tool-1",
			Name: "WebFetch",
			Args: []byte(`{"url":"https://missing.example","timeout":40}`),
		}},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()
	a.turn.PendingToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{CallID: "tool-1", Name: "WebFetch", ArgsJSON: `{"url":"https://missing.example","timeout":40}`})

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: turnID,
		Payload: &TurnCancelledPayload{
			TurnID: turnID,
			Calls:  []PendingToolCall{{CallID: "tool-1", Name: "WebFetch", ArgsJSON: `{"url":"https://missing.example","timeout":40}`}},
		},
	})
	a.flushPersist()

	msgs := a.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("len(GetMessages()) = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "assistant" || len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("first message = %#v, want assistant tool call", msgs[0])
	}
	if msgs[1].Role != "tool" || msgs[1].Content != "Cancelled" {
		t.Fatalf("second message = %#v, want cancelled tool result", msgs[1])
	}

	restored, err := a.recovery.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("len(restored main messages) = %d, want 2", len(restored))
	}
	if restored[1].Role != "tool" || restored[1].Content != "Cancelled" {
		t.Fatalf("restored tool message = %#v, want cancelled tool result", restored[1])
	}
}

func TestCancelCurrentTurnClosesSpeculativeToolCardWithoutPersistingToolMessage(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected active turn")
	}
	turnID := a.turn.ID
	a.turn.recordStreamingToolCall(PendingToolCall{
		CallID:   "stream-write-1",
		Name:     "Write",
		ArgsJSON: `{"path":".chord/plans/plan-002.md","content":"partial"}`,
	})

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: turnID,
		Payload: &TurnCancelledPayload{
			TurnID: turnID,
			Calls: []PendingToolCall{{
				CallID:   "stream-write-1",
				Name:     "Write",
				ArgsJSON: `{"path":".chord/plans/plan-002.md","content":"partial"}`,
			}},
		},
	})
	a.flushPersist()

	if msgs := a.GetMessages(); len(msgs) != 0 {
		t.Fatalf("len(GetMessages()) = %d, want 0 (speculative tool call must not persist)", len(msgs))
	}
	if restored, err := a.recovery.LoadMessages("main"); err != nil {
		t.Fatalf("LoadMessages(main): %v", err)
	} else if len(restored) != 0 {
		t.Fatalf("len(restored main messages) = %d, want 0", len(restored))
	}

	events := drainAgentEvents(a.Events())
	var sawCancelled bool
	for _, evt := range events {
		if res, ok := evt.(ToolResultEvent); ok {
			if res.CallID == "stream-write-1" {
				sawCancelled = true
				if res.Status != ToolResultStatusCancelled {
					t.Fatalf("ToolResultEvent status = %q, want %q", res.Status, ToolResultStatusCancelled)
				}
				if res.Result != "Cancelled" {
					t.Fatalf("ToolResultEvent result = %q, want Cancelled", res.Result)
				}
			}
		}
	}
	if !sawCancelled {
		t.Fatal("expected speculative Write tool card to be closed with cancelled result")
	}
}

func TestHandleTurnCancelledIgnoresStaleEventAfterNewTurn(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected first active turn")
	}
	staleTurnID := a.turn.ID

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected second active turn")
	}
	currentTurnID := a.turn.ID
	if currentTurnID == staleTurnID {
		t.Fatal("expected new turn ID")
	}

	a.handleTurnCancelled(Event{
		Type:   EventTurnCancelled,
		TurnID: staleTurnID,
		Payload: &TurnCancelledPayload{
			TurnID: staleTurnID,
		},
	})

	if a.turn == nil {
		t.Fatal("stale cancellation unexpectedly cleared current turn")
	}
	if a.turn.ID != currentTurnID {
		t.Fatalf("turn ID = %d, want %d", a.turn.ID, currentTurnID)
	}

	for {
		select {
		case evt := <-a.Events():
			if _, ok := evt.(RequestCycleStartedEvent); ok {
				continue
			}
			t.Fatalf("unexpected event after stale cancellation: %T", evt)
		default:
			return
		}
	}
}
