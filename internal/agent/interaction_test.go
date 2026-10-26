package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
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

// deliverTestConfirm marks a registered confirm as delivered through the real
// delivery path, with a no-op event emission.
func deliverTestConfirm(t *testing.T, b *interactionBroker, requestID string) {
	t.Helper()
	if err := b.deliverConfirm(context.Background(), requestID, func() error { return nil }); err != nil {
		t.Fatalf("deliver confirm %q: %v", requestID, err)
	}
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

	ch := a.interaction.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)

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

	ch := a.interaction.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)
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
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)
	defer b.unregisterConfirm("req-1")

	go b.resolveConfirm("req-1", ConfirmResponse{Approved: true})

	resp, outcome, err := b.awaitConfirm(context.Background(), "req-1", ch, time.Second, "Read")
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
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)
	defer b.unregisterConfirm("req-1")

	resp, outcome, err := b.awaitConfirm(context.Background(), "req-1", ch, time.Millisecond, "Read")
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
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)
	defer b.unregisterConfirm("req-1")

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	resp, outcome, err := b.awaitConfirm(ctx, "req-1", ch, time.Minute, "Read")
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

// TestCancellationSettlementKeepsBufferedAnswer pins the outcome consistency a
// turn cancellation owes a confirm the user answered before the abort: the
// settlement and the waiter both report the answer's outcome instead of the
// cancellation, and an answer that arrives after the decision is ignored.
func TestCancellationSettlementKeepsBufferedAnswer(t *testing.T) {
	b := newInteractionBroker(nil)
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "delete", AgentID: "main"}, ctx)
	defer b.unregisterConfirm("req-1")
	deliverTestConfirm(t, b, "req-1")

	b.resolveConfirm("req-1", ConfirmResponse{Approved: true})
	cancel()

	var outcomes []string
	b.emitCancelledConfirmResolutions(func(requestID string, _ confirmIdentity, outcome string) {
		if requestID != "req-1" {
			t.Fatalf("settled unexpected request %q", requestID)
		}
		outcomes = append(outcomes, outcome)
	})
	if !reflect.DeepEqual(outcomes, []string{ConfirmOutcomeApproved}) {
		t.Fatalf("settled outcomes = %v, want [%s]", outcomes, ConfirmOutcomeApproved)
	}

	resp, outcome, err := b.awaitConfirm(ctx, "req-1", ch, 0, "delete")
	if err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	if !resp.Approved || outcome != ConfirmOutcomeApproved {
		t.Fatalf("awaitConfirm = (%+v, %q), want approved", resp, outcome)
	}

	// A late answer must not replace the decision the settlement already
	// reported.
	b.resolveConfirm("req-1", ConfirmResponse{Approved: false})
	b.confirmMapMu.Lock()
	decision := b.confirmDecisions["req-1"]
	b.confirmMapMu.Unlock()
	if decision.outcome != ConfirmOutcomeApproved || !decision.resp.Approved {
		t.Fatalf("decision after late answer = %+v, want approved", decision)
	}
	select {
	case resp := <-ch:
		t.Fatalf("late answer delivered to the waiter: %+v", resp)
	default:
	}
}

// TestInteractionBrokerAwaitStops verifies an in-flight await unblocks with
// ErrAgentShutdown when stoppingCh closes.
func TestInteractionBrokerAwaitStops(t *testing.T) {
	stopCh := make(chan struct{})
	b := newInteractionBroker(stopCh)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)
	defer b.unregisterConfirm("req-1")

	close(stopCh)

	if _, _, err := b.awaitConfirm(context.Background(), "req-1", ch, 0, "Read"); err != ErrAgentShutdown {
		t.Fatalf("err = %v, want ErrAgentShutdown", err)
	}
}

// TestInteractionBrokerSettlementWaitsForClaimedWaiterEmission pins the
// cancellation ordering barrier: once a waiter has claimed the terminal
// resolution, the cancellation settlement must not finish (and let the turn
// queue its idle) until that resolution has actually been emitted.
func TestInteractionBrokerSettlementWaitsForClaimedWaiterEmission(t *testing.T) {
	b := newInteractionBroker(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "delete", AgentID: "main"}, ctx)
	deliverTestConfirm(t, b, "req-1")
	defer b.unregisterConfirm("req-1")

	emitStarted := make(chan struct{})
	release := make(chan struct{})
	waiterDone := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		b.emitConfirmResolvedOnce("req-1", func() {
			close(emitStarted)
			<-release
		})
		close(waiterDone)
	}()
	select {
	case <-emitStarted:
	case <-time.After(time.Second):
		t.Fatal("waiter did not start its claimed emission")
	}

	settleDone := make(chan struct{})
	var settled []string
	go func() {
		b.emitCancelledConfirmResolutions(func(requestID string, _ confirmIdentity, _ string) {
			settled = append(settled, requestID)
		})
		close(settleDone)
	}()
	select {
	case <-settleDone:
		t.Fatal("cancellation settlement finished before the waiter's claimed emission")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Fatal("waiter did not finish its claimed emission")
	}
	select {
	case <-settleDone:
	case <-time.After(time.Second):
		t.Fatal("cancellation settlement did not finish after the claimed emission")
	}
	if len(settled) != 0 {
		t.Fatalf("cancellation emitted %+v after the waiter held the claim, want none", settled)
	}
}

// TestDeliverConfirmCancelledContextSkipsRequest pins the delivery rule a
// cancellation relies on: a request whose context is already cancelled never
// reaches a consumer and is not marked delivered, so the settlement reports no
// resolution for a dialog that never opened.
func TestCancellationSettlementKeepsExpiredDeadlineAsTimeout(t *testing.T) {
	b := newInteractionBroker(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ch := b.registerConfirm("deadline-req", testWalltimeTarget("main"), confirmIdentity{ToolName: "delete", AgentID: "main"}, ctx)
	defer b.unregisterConfirm("deadline-req")
	if err := b.deliverConfirm(ctx, "deadline-req", func() error { return nil }); err != nil {
		t.Fatalf("deliverConfirm: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("deadline did not expire")
	}

	var emitted string
	b.emitCancelledConfirmResolutions(func(_ string, _ confirmIdentity, outcome string) {
		emitted = outcome
	})
	resp, outcome, err := b.awaitConfirm(ctx, "deadline-req", ch, time.Minute, "delete")
	if emitted != ConfirmOutcomeTimeout || outcome != ConfirmOutcomeTimeout || err != nil {
		t.Fatalf("deadline settlement emitted=%q waiter=%q err=%v; want timeout without error", emitted, outcome, err)
	}
	if resp.Approved {
		t.Fatal("expired deadline must auto-deny the confirm")
	}
}

func TestDeliverConfirmCancelledContextSkipsRequest(t *testing.T) {
	b := newInteractionBroker(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "delete", AgentID: "main"}, ctx)
	defer b.unregisterConfirm("req-1")

	emitted := false
	err := b.deliverConfirm(ctx, "req-1", func() error {
		emitted = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("deliverConfirm error = %v, want context.Canceled", err)
	}
	if emitted {
		t.Fatal("deliverConfirm emitted a request for a cancelled context")
	}
	b.emitCancelledConfirmResolutions(func(requestID string, _ confirmIdentity, _ string) {
		t.Fatalf("cancellation settled undelivered confirm %v", requestID)
	})
}

// TestCancellationSettlementWaitsForInFlightDelivery pins the ordering barrier
// between delivery and cancellation: the settlement shares confirmResolveMu
// with the delivery mark, so it cannot report a resolution while the request is
// still being queued. Without the shared lock, the request and its resolution
// could reach a consumer out of order.
func TestCancellationSettlementWaitsForInFlightDelivery(t *testing.T) {
	b := newInteractionBroker(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "delete", AgentID: "main"}, ctx)
	defer b.unregisterConfirm("req-1")

	var orderMu sync.Mutex
	var order []string
	record := func(step string) {
		orderMu.Lock()
		order = append(order, step)
		orderMu.Unlock()
	}

	emitting := make(chan struct{})
	release := make(chan struct{})
	delivered := make(chan error, 1)
	go func() {
		delivered <- b.deliverConfirm(ctx, "req-1", func() error {
			close(emitting)
			<-release
			record("request")
			return nil
		})
	}()
	select {
	case <-emitting:
	case <-time.After(time.Second):
		t.Fatal("confirm delivery did not start")
	}
	cancel()

	settled := make(chan struct{})
	go func() {
		b.emitCancelledConfirmResolutions(func(string, confirmIdentity, string) {
			record("resolved")
		})
		close(settled)
	}()
	select {
	case <-settled:
		t.Fatal("cancellation settlement finished while the request delivery was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case err := <-delivered:
		if err != nil {
			t.Fatalf("deliverConfirm error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery did not finish")
	}
	select {
	case <-settled:
	case <-time.After(time.Second):
		t.Fatal("cancellation settlement did not finish after the delivery")
	}

	orderMu.Lock()
	defer orderMu.Unlock()
	if !reflect.DeepEqual(order, []string{"request", "resolved"}) {
		t.Fatalf("event order = %v, want request before resolved", order)
	}
}

// TestInteractionBrokerClearPending drops in-flight mappings so a subsequent
// resolve is a no-op.
func TestInteractionBrokerClearPending(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"), confirmIdentity{ToolName: "read", AgentID: "main"}, nil)

	b.clearPending()
	b.resolveConfirm("req-1", ConfirmResponse{Approved: true})

	select {
	case <-ch:
		t.Fatal("expected no delivery after clearPending")
	default:
	}
}
