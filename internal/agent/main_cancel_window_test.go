package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/llm"
	"github.com/keakon/chord/internal/message"
)

// collectAgentEventsUntil reads events until want reports the sequence it is
// looking for, so a test can assert on event ordering without sleeping.
func collectAgentEventsUntil(t *testing.T, events <-chan AgentEvent, want func([]AgentEvent) bool) []AgentEvent {
	t.Helper()
	var collected []AgentEvent
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if want(collected) {
			return collected
		}
		select {
		case evt := <-events:
			collected = append(collected, evt)
		case <-deadline.C:
			t.Fatalf("timed out collecting agent events after %d: %#v", len(collected), collected)
		}
	}
}

func waitForAgentState(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// newCancelWindowTestAgent wires the readiness markers and a recording provider
// so a test can tell a cancelled turn (no request) from a running one.
func newCancelWindowTestAgent(t *testing.T, provider *keyRecordingProvider) *MainAgent {
	t.Helper()
	a := newReadyTestMainAgent(t)
	providerCfg := llm.NewProviderConfig("sample/test-provider", config.ProviderConfig{
		Type: config.ProviderTypeChatCompletions,
		Models: map[string]config.ModelConfig{
			"test-model": {Limit: config.ModelLimit{Context: 128000, Output: 4096}},
		},
	}, []string{"test-key"})
	a.swapLLMClientWithRef(llm.NewClient(providerCfg, provider, "test-model", 4096, "sys"), "test-model", 128000, "sample/test-provider/test-model")
	return a
}

// TestCancelCurrentTurnCoversMessageAcceptedBeforeItsTurn pins the window where
// a message is accepted but the loop has not created its turn yet. The cancel
// must keep the message in the transcript and close the turn as cancelled
// without asking the model, and it must still emit the busy marker a started
// turn produces plus the global idle that settles an ACP prompt.
func TestCancelCurrentTurnCoversMessageAcceptedBeforeItsTurn(t *testing.T) {
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)

	a.SendUserMessage("hello")
	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true for an accepted message whose turn does not exist yet")
	}

	startMainAgentLoopForTest(t, a)

	collectAgentEventsUntil(t, a.Events(), func(collected []AgentEvent) bool {
		started := false
		for _, evt := range collected {
			switch evt.(type) {
			case RequestCycleStartedEvent:
				started = true
			case GlobalIdleEvent:
				if started {
					return true
				}
			}
		}
		return false
	})

	if requests := provider.recorded(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want 0 for a message cancelled before its first request", len(requests))
	}
	if turn := a.currentTurn(); turn != nil {
		t.Fatalf("turn = %#v, want the cancelled turn to be cleared", turn)
	}
	msgs := a.GetMessages()
	if len(msgs) != 1 || msgs[0].Role != message.RoleUser || msgs[0].Content != "hello" {
		t.Fatalf("messages = %#v, want the retained user message", msgs)
	}
}

// TestCancelCurrentTurnCoversOnlyMessagesAcceptedBeforeIt pins the other half of
// the watermark: a message the user sent after the cancel was accepted outranks
// it and still runs.
func TestCancelCurrentTurnCoversOnlyMessagesAcceptedBeforeIt(t *testing.T) {
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)

	a.SendUserMessage("first")
	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true for the accepted message")
	}
	a.SendUserMessage("second")

	startMainAgentLoopForTest(t, a)
	waitForAgentState(t, "the message accepted after the cancel to be answered", func() bool {
		msgs := a.GetMessages()
		return len(msgs) == 3 && msgs[2].Role == message.RoleAssistant
	})

	if requests := provider.recorded(); len(requests) != 1 {
		t.Fatalf("provider requests = %d, want only the message accepted after the cancel", len(requests))
	}
	msgs := a.GetMessages()
	if msgs[0].Role != message.RoleUser || msgs[0].Content != "first" {
		t.Fatalf("first message = %#v, want the cancelled message retained ahead of the second", msgs[0])
	}
	if msgs[1].Role != message.RoleUser || msgs[1].Content != "second" {
		t.Fatalf("second message = %#v, want the later user message", msgs[1])
	}
}

// TestCancelCurrentTurnWhileIdleLeavesLaterMessagesAlone pins that an idle
// cancel stays idle: it reports false and arms nothing that would cancel the
// next message the user sends.
func TestCancelCurrentTurnWhileIdleLeavesLaterMessagesAlone(t *testing.T) {
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)
	startMainAgentLoopForTest(t, a)

	if cancelled := a.CancelCurrentTurn(); cancelled {
		t.Fatal("CancelCurrentTurn() = true with no turn and nothing accepted")
	}

	a.SendUserMessage("hello")
	waitForAgentState(t, "the later message to be answered", func() bool {
		msgs := a.GetMessages()
		return len(msgs) == 2 && msgs[1].Role == message.RoleAssistant
	})
	if requests := provider.recorded(); len(requests) != 1 {
		t.Fatalf("provider requests = %d, want the later message to run", len(requests))
	}
}

// TestCancelCurrentTurnWithActiveTurnArmsNoWatermark pins that the active-turn
// path is unchanged: the turn is aborted directly, and a message queued behind
// it keeps the ordinary queued-cancel semantics instead of being matched against
// a message order.
func TestCancelCurrentTurnWithActiveTurnArmsNoWatermark(t *testing.T) {
	a := newReadyTestMainAgent(t)

	a.newTurn()
	if a.turn == nil {
		t.Fatal("expected an active turn")
	}
	a.SendUserMessage("queued while busy")

	if cancelled := a.CancelCurrentTurn(); !cancelled {
		t.Fatal("CancelCurrentTurn() = false, want true for an active turn")
	}
	if got := a.cancelUpTo.Load(); got != 0 {
		t.Fatalf("cancelUpTo = %d, want 0: an active turn is cancelled directly, never by message order", got)
	}
}

// TestCancelRequestOutrankedByTurnOriginDoesNotAbortIt pins the ordering race
// between a cancel request and message acceptance: the request sampled the
// watermark before the message existed, but the message's event reached the loop
// first. The turn belongs to a message accepted after the cancel, so the queued
// request must not abort it.
func TestCancelRequestOutrankedByTurnOriginDoesNotAbortIt(t *testing.T) {
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)

	a.SendUserMessage("later")
	// The request is queued behind a message it cannot cover, which is what a
	// cancel preempted between sampling the watermark and queueing produces.
	a.sendEvent(Event{Type: EventTurnCancelRequested, Payload: &turnCancelRequestPayload{AcceptedUpTo: 0}})

	startMainAgentLoopForTest(t, a)
	waitForAgentState(t, "the message accepted after the cancel to be answered", func() bool {
		msgs := a.GetMessages()
		return len(msgs) == 2 && msgs[1].Role == message.RoleAssistant
	})
	if requests := provider.recorded(); len(requests) != 1 {
		t.Fatalf("provider requests = %d, want only the message accepted after the cancel", len(requests))
	}
}

// TestCancelRequestAbortsATurnItsMessageWasAcceptedUnder pins the other side of
// the gate: a request that does cover the message which started the turn in
// front of it still cancels that turn.
func TestCancelRequestAbortsATurnItsMessageWasAcceptedUnder(t *testing.T) {
	a := newReadyTestMainAgent(t)

	// The loop already started this turn for a message the cancel covers, which
	// is the interleaving where the message does not wait in the queue.
	a.newTurn()
	a.turn.originAcceptedOrder = 1

	a.handleTurnCancelRequested(Event{
		Type:    EventTurnCancelRequested,
		Payload: &turnCancelRequestPayload{AcceptedUpTo: 1},
	})

	if a.turn != nil {
		t.Fatalf("turn = %#v, want the covered turn cancelled", a.turn)
	}
}

// TestCancelCoversQueuedMessageThatNeverStartedATurn pins the queueing half of
// the watermark: a message accepted before the cancel but unable to start a turn
// (an MCP transition blocks new turns) waits in the queue, and the drain must
// keep it in the transcript without asking the model. The turn it opens still
// reports the busy marker and the global idle an ACP prompt settles on.
func TestCancelCoversQueuedMessageThatNeverStartedATurn(t *testing.T) {
	provider := &keyRecordingProvider{}
	a := newCancelWindowTestAgent(t, provider)

	a.mcpTransitionActive.Store(true)
	a.SendUserMessage("queued")
	startMainAgentLoopForTest(t, a)
	waitForAgentState(t, "the queued message to reach the loop", func() bool {
		return a.dispatchedRawMessages.Load() >= 1
	})

	if cancelled := a.CancelCurrentTurn(); cancelled {
		t.Fatal("CancelCurrentTurn() = true, want false: the message was accepted and already dispatched")
	}

	// The transition finishing is what drains the queue.
	a.sendEvent(Event{Type: EventMCPControlDone, Payload: mcpControlDonePayload{
		readyGen: a.ResetMCPReady(),
		req:      MCPControlRequest{Action: MCPControlEnable},
	}})

	collectAgentEventsUntil(t, a.Events(), func(collected []AgentEvent) bool {
		started, settled := false, false
		for _, evt := range collected {
			switch evt.(type) {
			case RequestCycleStartedEvent:
				started = true
			case GlobalIdleEvent:
				settled = settled || started
			}
		}
		return settled
	})

	if requests := provider.recorded(); len(requests) != 0 {
		t.Fatalf("provider requests = %d, want 0: a message the cancel covered must not run", len(requests))
	}
	msgs := a.GetMessages()
	if len(msgs) != 1 || msgs[0].Role != message.RoleUser || msgs[0].Content != "queued" {
		t.Fatalf("messages = %#v, want the covered message kept and nothing else", msgs)
	}
}
