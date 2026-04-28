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
			Name: "WebFetch",
			Args: []byte(`{"url":"https://slow.example"}`),
		}},
	}
	a.ctxMgr.Append(assistant)
	a.persistAsync("main", assistant)
	a.flushPersist()
	a.turn.PendingToolCalls.Store(1)
	a.turn.recordPendingToolCall(PendingToolCall{
		CallID:   "tool-user-cancel",
		Name:     "WebFetch",
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
			Name: "WebFetch",
			Args: []byte(`{"url":"https://slow.example"}`),
		}},
	}
	sub.ctxMgr.Append(assistant)
	if err := a.recovery.PersistMessage(sub.instanceID, assistant); err != nil {
		t.Fatalf("PersistMessage(sub assistant): %v", err)
	}
	sub.turn.PendingToolCalls.Store(1)
	sub.turn.recordPendingToolCall(PendingToolCall{
		CallID:   "tool-sub-interrupt",
		Name:     "WebFetch",
		ArgsJSON: `{"url":"https://slow.example"}`,
		AgentID:  sub.instanceID,
	})
	a.mu.Lock()
	a.subAgents[sub.instanceID] = sub
	a.mu.Unlock()

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true")
	}

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

	restored, err := a.recovery.LoadMessages(sub.instanceID)
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
