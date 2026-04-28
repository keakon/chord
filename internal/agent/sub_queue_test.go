package agent

import (
	"context"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestSubAgentInputOverflowPreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := &SubAgent{
		instanceID: "agent-1",
		parentCtx:  ctx,
		inputCh:    make(chan pendingUserMessage, 1),
	}

	sub.enqueueUserMessage(pendingUserMessage{Content: "one"})
	sub.enqueueUserMessage(pendingUserMessage{Content: "two"})
	sub.enqueueUserMessage(pendingUserMessage{Content: "three"})

	if got := (<-sub.inputCh).Content; got != "one" {
		t.Fatalf("first dequeued content = %q, want one", got)
	}
	sub.refillInputChannelFromOverflow()
	if got := (<-sub.inputCh).Content; got != "two" {
		t.Fatalf("second dequeued content = %q, want two", got)
	}
	sub.refillInputChannelFromOverflow()
	if got := (<-sub.inputCh).Content; got != "three" {
		t.Fatalf("third dequeued content = %q, want three", got)
	}
}

func TestSubAgentContextAppendOverflowPreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := &SubAgent{
		instanceID:  "agent-1",
		parentCtx:   ctx,
		ctxAppendCh: make(chan message.Message, 1),
	}

	if !sub.TryEnqueueContextAppend(message.Message{Content: "one"}) {
		t.Fatal("expected first context append to enqueue")
	}
	if !sub.TryEnqueueContextAppend(message.Message{Content: "two"}) {
		t.Fatal("expected second context append to buffer")
	}
	if !sub.TryEnqueueContextAppend(message.Message{Content: "three"}) {
		t.Fatal("expected third context append to buffer")
	}

	if got := (<-sub.ctxAppendCh).Content; got != "one" {
		t.Fatalf("first dequeued context append = %q, want one", got)
	}
	sub.refillContextAppendChannelFromOverflow()
	if got := (<-sub.ctxAppendCh).Content; got != "two" {
		t.Fatalf("second dequeued context append = %q, want two", got)
	}
	sub.refillContextAppendChannelFromOverflow()
	if got := (<-sub.ctxAppendCh).Content; got != "three" {
		t.Fatalf("third dequeued context append = %q, want three", got)
	}
}

func TestSubAgentHandleContinueDrainsQueuedContextAppendsFirst(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")

	if !sub.TryEnqueueContextAppend(message.Message{Content: "background note"}) {
		t.Fatal("expected context append to enqueue")
	}

	sub.handleContinue()

	msgs := sub.ctxMgr.Snapshot()
	if len(msgs) == 0 {
		t.Fatal("expected queued context append to be committed before continue")
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.Content != "background note" {
		t.Fatalf("last message = %#v, want user background note", last)
	}
}
