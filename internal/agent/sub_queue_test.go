package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestSubAgentInputOverflowPreservesOrder(t *testing.T) {
	ctx := t.Context()

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

func TestSubAgentBusyTurnDefersQueuedUserInput(t *testing.T) {
	sub := &SubAgent{
		inputCh: make(chan pendingUserMessage, 1),
		turn:    &Turn{ID: 1},
	}
	sub.setState(SubAgentStateRunning, "working")
	sub.inputCh <- pendingUserMessage{DraftID: "draft-1", Content: "follow up", FromUser: true}
	sub.llmRequestInFlight.Store(true)

	if sub.canStartUserTurn() {
		t.Fatal("canStartUserTurn() = true during an in-flight request")
	}
	if got := len(sub.inputCh); got != 1 {
		t.Fatalf("queued input count = %d, want 1", got)
	}
}

func TestSubAgentTerminalStateDefersQueuedUserInputUntilReactivated(t *testing.T) {
	for _, state := range []SubAgentState{SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled} {
		t.Run(string(state), func(t *testing.T) {
			sub := &SubAgent{
				inputCh: make(chan pendingUserMessage, 1),
				turn:    &Turn{ID: 1},
			}
			sub.setState(state, "stopped")
			sub.inputCh <- pendingUserMessage{DraftID: "draft-1", Content: "follow up", FromUser: true}

			if sub.canStartUserTurn() {
				t.Fatalf("canStartUserTurn() = true while state=%q", state)
			}
			if got := len(sub.inputCh); got != 1 {
				t.Fatalf("queued input count = %d, want 1", got)
			}

			sub.setState(SubAgentStateRunning, "resumed")
			if !sub.canStartUserTurn() {
				t.Fatal("canStartUserTurn() = false after explicit reactivation")
			}
		})
	}
}

func TestSubAgentContinueDoesNotReplaceRunningTurn(t *testing.T) {
	sub := &SubAgent{
		turn:       &Turn{ID: 7},
		continueCh: make(chan continueMsg, 1),
	}
	sub.continueCh <- continueMsg{}

	if !sub.tryHandleContinueSignal() {
		t.Fatal("continue signal was not handled")
	}
	if sub.turn == nil || sub.turn.ID != 7 {
		t.Fatalf("running turn = %#v, want original turn 7", sub.turn)
	}
}

func TestSubAgentRestartContinueWaitsForInFlightRequest(t *testing.T) {
	sub := &SubAgent{
		turn:       &Turn{ID: 7},
		continueCh: make(chan continueMsg, 1),
	}
	sub.llmRequestInFlight.Store(true)
	sub.continueCh <- continueMsg{restartStoppedTurn: true, drainContextAppends: true}

	if !sub.tryHandleContinueSignal() {
		t.Fatal("continue signal was not handled")
	}
	if sub.pendingContinue == nil || !sub.pendingContinue.restartStoppedTurn || !sub.pendingContinue.drainContextAppends {
		t.Fatalf("pendingContinue = %#v, want deferred restart", sub.pendingContinue)
	}
	if sub.tryHandlePendingContinue() {
		t.Fatal("pending restart ran before the in-flight request exited")
	}

	sub.llmRequestInFlight.Store(false)
	if sub.pendingContinue == nil {
		t.Fatal("pending restart was lost when the request exited")
	}
}

func TestSubAgentContextAppendOverflowPreservesOrder(t *testing.T) {
	ctx := t.Context()

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
