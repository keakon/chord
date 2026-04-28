package agent

import (
	"fmt"
	"testing"
	"time"

	"github.com/keakon/chord/internal/message"
)

func TestProcessPendingUserMessagesBeforeLLMInTurnEmitsConsumedEvent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.handleUserMessage(Event{Payload: "first"})
	if a.turn == nil {
		t.Fatal("expected active turn after first user message")
	}
	a.handlePendingDraftUpsert(Event{
		Payload: pendingUserMessageFromDraft("draft-1", []message.ContentPart{{Type: "text", Text: "queued"}}),
	})

	a.processPendingUserMessagesBeforeLLMInTurn()

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 2 || msgs[1].Role != "user" || msgs[1].Content != "queued" {
		t.Fatalf("messages after pending consume = %+v, want second user message 'queued'", msgs)
	}

	ev := nextNonRequestCycleEvent(t, a.Events())
	consumed, ok := ev.(PendingDraftConsumedEvent)
	if !ok {
		t.Fatalf("event type = %T, want PendingDraftConsumedEvent", ev)
	}
	if consumed.DraftID != "draft-1" {
		t.Fatalf("DraftID = %q, want draft-1", consumed.DraftID)
	}
	if len(consumed.Parts) != 1 || consumed.Parts[0].Text != "queued" {
		t.Fatalf("Parts = %+v, want single queued text part", consumed.Parts)
	}
}

func TestHandlePendingDraftRemoveDeletesMirroredQueueItem(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.handleUserMessage(Event{Payload: "first"})
	a.handlePendingDraftUpsert(Event{
		Payload: pendingUserMessageFromDraft("draft-1", []message.ContentPart{{Type: "text", Text: "one"}}),
	})
	a.handlePendingDraftUpsert(Event{
		Payload: pendingUserMessageFromDraft("draft-2", []message.ContentPart{{Type: "text", Text: "two"}}),
	})

	a.handlePendingDraftRemove(Event{
		Payload: "draft-1",
	})

	if len(a.pendingUserMessages) != 1 {
		t.Fatalf("len(pendingUserMessages) = %d, want 1", len(a.pendingUserMessages))
	}
	if got := a.pendingUserMessages[0].DraftID; got != "draft-2" {
		t.Fatalf("remaining DraftID = %q, want draft-2", got)
	}
}

func TestHandlePendingDraftUpsertWhenIdleEmitsConsumedEvent(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.handlePendingDraftUpsert(Event{
		Payload: pendingUserMessageFromDraft("draft-1", []message.ContentPart{{Type: "text", Text: "queued"}}),
	})

	msgs := a.ctxMgr.Snapshot()
	if len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Content != "queued" {
		t.Fatalf("messages after idle pending consume = %+v, want single user message 'queued'", msgs)
	}
	ev := nextNonRequestCycleEvent(t, a.Events())
	if _, ok := ev.(PendingDraftConsumedEvent); !ok {
		t.Fatalf("event type = %T, want PendingDraftConsumedEvent", ev)
	}
}

func TestHandleUserMessageWhenBusyDoesNotDropQueuedUserInput(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.handleUserMessage(Event{Payload: "first"})
	if a.turn == nil {
		t.Fatal("expected active turn after first user message")
	}
	for i := 0; i < 96; i++ {
		a.handleUserMessage(Event{Payload: fmt.Sprintf("queued-%02d", i)})
	}

	if got := len(a.pendingUserMessages); got != 96 {
		t.Fatalf("len(pendingUserMessages) = %d, want 96", got)
	}
	if got := a.pendingUserMessages[0].Content; got != "queued-00" {
		t.Fatalf("first queued content = %q, want queued-00", got)
	}
	if got := a.pendingUserMessages[len(a.pendingUserMessages)-1].Content; got != "queued-95" {
		t.Fatalf("last queued content = %q, want queued-95", got)
	}
}

func TestHandlePendingDraftUpsertWhenBusyDoesNotDropAtLargeQueueDepth(t *testing.T) {
	projectRoot := t.TempDir()
	a := newTestMainAgent(t, projectRoot)

	a.handleUserMessage(Event{Payload: "first"})
	if a.turn == nil {
		t.Fatal("expected active turn after first user message")
	}
	for i := 0; i < 96; i++ {
		a.handleUserMessage(Event{Payload: fmt.Sprintf("queued-%02d", i)})
	}
	a.handlePendingDraftUpsert(Event{
		Payload: pendingUserMessageFromDraft("draft-large", []message.ContentPart{{Type: "text", Text: "draft payload"}}),
	})

	if got := len(a.pendingUserMessages); got != 97 {
		t.Fatalf("len(pendingUserMessages) = %d, want 97", got)
	}
	last := a.pendingUserMessages[len(a.pendingUserMessages)-1]
	if last.DraftID != "draft-large" {
		t.Fatalf("last DraftID = %q, want draft-large", last.DraftID)
	}
}

func nextAgentEvent(t *testing.T, events <-chan AgentEvent) AgentEvent {
	t.Helper()
	select {
	case ev := <-events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for agent event")
		return nil
	}
}
