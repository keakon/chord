package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/keakon/chord/internal/llm"
)

// drainInternalEventTypes drains the agent's internal event bus, counting events
// by their Type string.
func drainInternalEventTypes(a *MainAgent) map[string]int {
	counts := make(map[string]int)
	for {
		select {
		case evt := <-a.eventCh:
			counts[evt.Type]++
		default:
			return counts
		}
	}
}

// waitForSubAgentConnecting reports whether a "connecting" activity for agentID
// appears on the event stream within timeout. asyncCallLLM emits this before the
// LLM call, so it is a reliable signal that a (re)started request was launched.
func waitForSubAgentConnecting(events <-chan AgentEvent, agentID string, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			if act, ok := ev.(AgentActivityEvent); ok && act.AgentID == agentID && act.Type == ActivityConnecting {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// TestSubAgentRoutingInvalidatedRestartsInsteadOfError verifies that when a
// sub-agent receives a routing-invalidated provider error, it restarts the
// request with the latest client instead of failing the turn with
// EventAgentError. Mirrors the MainAgent routing-invalidated path.
func TestSubAgentRoutingInvalidatedRestartsInsteadOfError(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := newControllableTestSubAgent(t, a, "task-1")
	// The restarted request parks in retry backoff under the stub provider;
	// cancel the sub-agent so its goroutine unwinds at test end.
	t.Cleanup(func() {
		if sub.cancel != nil {
			sub.cancel()
		}
	})
	sub.newTurn()
	drainInternalEventTypes(a)   // clear setup internal events
	drainAgentEvents(a.Events()) // clear setup TUI events

	// Routing-invalidated must restart via asyncCallLLM (observable as a fresh
	// "connecting" activity for this sub-agent) rather than fail the turn.
	sub.handleLLMResponse(&llmResult{
		err:    &llm.RoutingInvalidatedError{StartedGeneration: 1, CurrentGeneration: 2},
		turnID: sub.turn.ID,
	})
	if !waitForSubAgentConnecting(a.Events(), sub.instanceID, 2*time.Second) {
		t.Fatal("routing-invalidated did not restart the request (no connecting activity)")
	}
	if got := drainInternalEventTypes(a)[EventAgentError]; got != 0 {
		t.Fatalf("routing-invalidated produced %d EventAgentError, want 0 (should restart, not fail)", got)
	}

	// Control: a plain error must still surface EventAgentError.
	sub.newTurn()
	sub.handleLLMResponse(&llmResult{err: errors.New("boom"), turnID: sub.turn.ID})
	if got := drainInternalEventTypes(a)[EventAgentError]; got != 1 {
		t.Fatalf("plain error produced %d EventAgentError, want 1", got)
	}
}

// TestMainModelPoolSwitchDoesNotAffectSubAgent guards the invariant that
// switching the main role's model pool (focus on the main agent) leaves every
// sub-agent's LLM client untouched.
func TestMainModelPoolSwitchDoesNotAffectSubAgent(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	installPoolPolicyForTest(t, a)
	if err := a.ApplyInitialModel("provider/model-a"); err != nil {
		t.Fatalf("ApplyInitialModel: %v", err)
	}
	drainAgentEvents(a.Events())

	sub := newControllableTestSubAgent(t, a, "task-1")
	beforeClient := sub.llmClient
	beforeRef := sub.llmClient.PrimaryModelRef()

	a.newTurn()
	a.SetCurrentModelPool("fast") // focus is on the main agent (no focused sub-agent)
	dispatchPendingEvents(t, a)

	if got := a.ProviderModelRef(); got != "provider/model-b" {
		t.Fatalf("main ProviderModelRef after switch = %q, want provider/model-b", got)
	}
	if sub.llmClient != beforeClient {
		t.Fatal("main pool switch swapped the sub-agent llm client")
	}
	if got := sub.llmClient.PrimaryModelRef(); got != beforeRef {
		t.Fatalf("sub-agent model ref changed by main pool switch = %q, want %q", got, beforeRef)
	}
	if _, ok := a.ModelPoolPolicy().AgentOverride("worker"); ok {
		t.Fatal("main pool switch set an agent override for the sub-agent")
	}
}
