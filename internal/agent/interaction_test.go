package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/keakon/chord/internal/permission"
)

// testWalltimeTarget builds a walltime target pinned to a throwaway recorder,
// so broker tests can exercise the target-based settlement path without a
// full MainAgent.
func testWalltimeTarget(agentID string) *walltimeTarget {
	return newWalltimeRecorder(nil, nil, nil).captureAt(agentID, "", 0)
}

// collectConfirmEvents drains the agent output until both a confirm request
// and its resolution arrived, or the deadline passes.
func collectConfirmEvents(t *testing.T, a *MainAgent) (ConfirmRequestEvent, ConfirmResolvedEvent, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var request ConfirmRequestEvent
	var resolved ConfirmResolvedEvent
	for time.Now().Before(deadline) {
		for _, evt := range drainAgentEvents(a.outputCh) {
			switch e := evt.(type) {
			case ConfirmRequestEvent:
				request = e
			case ConfirmResolvedEvent:
				resolved = e
			}
		}
		if request.RequestID != "" && resolved.RequestID != "" {
			return request, resolved, true
		}
		time.Sleep(time.Millisecond)
	}
	return request, resolved, false
}

// TestAwaitConfirmTimeoutEmitsResolvedEvent pins the terminal push a client
// without a local countdown depends on: when the wait deadline fires and the
// call is auto-denied, the request id is reported as resolved with the timeout
// outcome so stale prompts can be retired instead of answered.
func TestAwaitConfirmTimeoutEmitsResolvedEvent(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())

	done := make(chan ConfirmResponse, 1)
	go func() {
		resp, err := a.AwaitConfirm(context.Background(), "Read", `{"path":"x"}`, 20*time.Millisecond, nil, nil)
		if err != nil {
			t.Errorf("AwaitConfirm: %v", err)
		}
		done <- resp
	}()

	request, resolved, ok := collectConfirmEvents(t, a)
	if !ok {
		t.Fatal("confirm request and resolution events not both emitted")
	}
	if resolved.RequestID != request.RequestID {
		t.Fatalf("resolved request id = %q, want the requested %q", resolved.RequestID, request.RequestID)
	}
	if resolved.Outcome != ConfirmOutcomeTimeout {
		t.Fatalf("outcome = %q, want %q", resolved.Outcome, ConfirmOutcomeTimeout)
	}
	if resolved.ToolName != "Read" || resolved.AgentID != "main" {
		t.Fatalf("resolved event = %+v, want tool Read for main", resolved)
	}
	select {
	case resp := <-done:
		if resp.Approved {
			t.Fatal("timeout must auto-deny")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitConfirm did not return after the resolution event")
	}
}

// TestAwaitConfirmAnswerEmitsResolvedEvent covers the answered path: a client
// resolution is echoed as an approved terminal event for the same request id.
func TestAwaitConfirmAnswerEmitsResolvedEvent(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())

	done := make(chan ConfirmResponse, 1)
	go func() {
		resp, err := a.AwaitConfirm(context.Background(), "Read", `{"path":"x"}`, 5*time.Second, nil, nil)
		if err != nil {
			t.Errorf("AwaitConfirm: %v", err)
		}
		done <- resp
	}()

	var request ConfirmRequestEvent
	deadline := time.Now().Add(5 * time.Second)
	for request.RequestID == "" && time.Now().Before(deadline) {
		for _, evt := range drainAgentEvents(a.outputCh) {
			if e, ok := evt.(ConfirmRequestEvent); ok {
				request = e
			}
		}
		if request.RequestID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if request.RequestID == "" {
		t.Fatal("confirm request not emitted")
	}
	a.ResolveConfirm("allow", `{"path":"x"}`, "", "", request.RequestID)

	var resolved ConfirmResolvedEvent
	deadline = time.Now().Add(5 * time.Second)
	for resolved.RequestID == "" && time.Now().Before(deadline) {
		for _, evt := range drainAgentEvents(a.outputCh) {
			if e, ok := evt.(ConfirmResolvedEvent); ok {
				resolved = e
			}
		}
		if resolved.RequestID == "" {
			time.Sleep(time.Millisecond)
		}
	}
	if resolved.RequestID == "" {
		t.Fatal("resolution event not emitted after answering")
	}
	if resolved.RequestID != request.RequestID || resolved.Outcome != ConfirmOutcomeApproved {
		t.Fatalf("resolved event = %+v, want id %q approved", resolved, request.RequestID)
	}
	select {
	case resp := <-done:
		if !resp.Approved {
			t.Fatal("expected the allow answer to reach the waiter")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitConfirm did not return after answering")
	}
}

func TestResolveConfirmUnknownActionDenies(t *testing.T) {
	a := &MainAgent{interaction: newInteractionBroker(nil)}

	ch := a.interaction.registerConfirm("req-1", testWalltimeTarget("main"))

	a.ResolveConfirm("bogus", `{"path":"x"}`, "", "", "req-1")

	select {
	case resp := <-ch:
		if resp.Approved {
			t.Fatal("expected unknown action to be denied")
		}
	default:
		t.Fatal("expected confirm response to be delivered")
	}
}

func TestResolveConfirmWithRuleIntentPassesIntent(t *testing.T) {
	a := &MainAgent{interaction: newInteractionBroker(nil)}

	ch := a.interaction.registerConfirm("req-1", testWalltimeTarget("main"))
	intent := &ConfirmRuleIntent{
		Rules: []ConfirmRule{{Pattern: "git *"}},
		Scope: int(permission.ScopeProject),
	}

	a.ResolveConfirmWithRuleIntent("allow", `{"command":"git status"}`, "", "", "req-1", intent)

	select {
	case resp := <-ch:
		if !resp.Approved {
			t.Fatal("expected response to be approved")
		}
		if resp.RuleIntent == nil {
			t.Fatal("expected rule intent to be propagated")
		}
		if !reflect.DeepEqual(resp.RuleIntent.Rules, []ConfirmRule{{Pattern: "git *"}}) || resp.RuleIntent.Scope != int(permission.ScopeProject) {
			t.Fatalf("rule intent = %+v, want pattern=git *, scope=%d", resp.RuleIntent, int(permission.ScopeProject))
		}
	default:
		t.Fatal("expected confirm response to be delivered")
	}
}

// TestInteractionBrokerResolveUnknownIsNoop verifies that resolving a request
// with no registered waiter (already resolved, cleared, or unknown) is a safe
// no-op rather than a panic or block.
func TestInteractionBrokerResolveUnknownIsNoop(t *testing.T) {
	b := newInteractionBroker(nil)
	b.resolveConfirm("missing", ConfirmResponse{Approved: true})
}

// TestInteractionBrokerAwaitConfirmResolves verifies the register→await→resolve
// round trip without standing up a full MainAgent.
func TestInteractionBrokerAwaitConfirmResolves(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))
	defer b.unregisterConfirm("req-1")

	go b.resolveConfirm("req-1", ConfirmResponse{Approved: true})

	resp, outcome, err := b.awaitConfirm(context.Background(), ch, time.Second, "Read")
	if err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	if !resp.Approved {
		t.Fatal("expected approved response")
	}
	if outcome != ConfirmOutcomeApproved {
		t.Fatalf("outcome = %q, want %q", outcome, ConfirmOutcomeApproved)
	}
}

// TestInteractionBrokerAwaitConfirmTimeoutDenies verifies that a confirm await
// auto-denies (no error) when the timeout fires.
func TestInteractionBrokerAwaitConfirmTimeoutDenies(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))
	defer b.unregisterConfirm("req-1")

	resp, outcome, err := b.awaitConfirm(context.Background(), ch, time.Millisecond, "Read")
	if err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	if resp.Approved {
		t.Fatal("expected timeout to auto-deny")
	}
	if outcome != ConfirmOutcomeTimeout {
		t.Fatalf("outcome = %q, want %q", outcome, ConfirmOutcomeTimeout)
	}
}

// TestInteractionBrokerAwaitConfirmDeadlineIsTimeout verifies a context
// deadline settles as the timeout outcome instead of a user cancellation, so a
// caller that bounds both the queue wait and the modal wait reports one
// deterministic terminal state.
func TestInteractionBrokerAwaitConfirmDeadlineIsTimeout(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))
	defer b.unregisterConfirm("req-1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	resp, outcome, err := b.awaitConfirm(ctx, ch, time.Minute, "Read")
	if err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	if resp.Approved {
		t.Fatal("deadline must auto-deny")
	}
	if outcome != ConfirmOutcomeTimeout {
		t.Fatalf("outcome = %q, want %q", outcome, ConfirmOutcomeTimeout)
	}
}

// TestInteractionBrokerAwaitStops verifies an in-flight await unblocks with
// ErrAgentShutdown when stoppingCh closes.
func TestInteractionBrokerAwaitStops(t *testing.T) {
	stopCh := make(chan struct{})
	b := newInteractionBroker(stopCh)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))
	defer b.unregisterConfirm("req-1")

	close(stopCh)

	if _, _, err := b.awaitConfirm(context.Background(), ch, 0, "Read"); err != ErrAgentShutdown {
		t.Fatalf("err = %v, want ErrAgentShutdown", err)
	}
}

// TestInteractionBrokerClearPending drops in-flight mappings so a subsequent
// resolve is a no-op.
func TestInteractionBrokerClearPending(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))

	b.clearPending()
	b.resolveConfirm("req-1", ConfirmResponse{Approved: true})

	select {
	case <-ch:
		t.Fatal("expected no delivery after clearPending")
	default:
	}
}
