package agent

import (
	"context"
	"fmt"
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
		Patterns: []string{"git *"},
		Scope:    int(permission.ScopeProject),
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
		if !reflect.DeepEqual(resp.RuleIntent.Patterns, []string{"git *"}) || resp.RuleIntent.Scope != int(permission.ScopeProject) {
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
	b.resolveQuestion("missing", QuestionResponse{})
}

// TestInteractionBrokerAwaitConfirmResolves verifies the register→await→resolve
// round trip without standing up a full MainAgent.
func TestInteractionBrokerAwaitConfirmResolves(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))
	defer b.unregisterConfirm("req-1")

	go b.resolveConfirm("req-1", ConfirmResponse{Approved: true})

	resp, err := b.awaitConfirm(context.Background(), ch, time.Second, "Read")
	if err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	if !resp.Approved {
		t.Fatal("expected approved response")
	}
}

// TestInteractionBrokerAwaitConfirmTimeoutDenies verifies that a confirm await
// auto-denies (no error) when the timeout fires.
func TestInteractionBrokerAwaitConfirmTimeoutDenies(t *testing.T) {
	b := newInteractionBroker(nil)
	ch := b.registerConfirm("req-1", testWalltimeTarget("main"))
	defer b.unregisterConfirm("req-1")

	resp, err := b.awaitConfirm(context.Background(), ch, time.Millisecond, "Read")
	if err != nil {
		t.Fatalf("awaitConfirm: %v", err)
	}
	if resp.Approved {
		t.Fatal("expected timeout to auto-deny")
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

	if _, err := b.awaitConfirm(context.Background(), ch, 0, "Read"); err != ErrAgentShutdown {
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

// TestInteractionBrokerConcurrentConfirmAndQuestionFlows registers and settles
// confirm and question waits from parallel goroutines. The two flows use
// independent flow locks and independent agent-id maps, so this must never
// race; run under -race it guards against a regression where both flows shared
// one map under two different locks.
func TestInteractionBrokerConcurrentConfirmAndQuestionFlows(t *testing.T) {
	b := newInteractionBroker(nil)
	var settledMu sync.Mutex
	settled := make(map[string]time.Duration)
	b.setSettledHook(func(target *walltimeTarget, d time.Duration) {
		settledMu.Lock()
		settled[target.agentID] += d
		settledMu.Unlock()
	})

	var wg sync.WaitGroup
	const perFlow = 200
	for i := range perFlow {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			req := fmt.Sprintf("c-%d", n)
			ch := b.registerConfirm(req, testWalltimeTarget("main-confirm"))
			defer b.unregisterConfirm(req)
			b.resolveConfirm(req, ConfirmResponse{Approved: true})
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Error("confirm wait not resolved")
			}
		}(i)
		go func(n int) {
			defer wg.Done()
			req := fmt.Sprintf("q-%d", n)
			ch := b.registerQuestion(req, testWalltimeTarget("worker-question"))
			defer b.unregisterQuestion(req)
			b.resolveQuestion(req, QuestionResponse{Answers: []string{"yes"}})
			select {
			case <-ch:
			case <-time.After(time.Second):
				t.Error("question wait not resolved")
			}
		}(i)
	}
	wg.Wait()

	settledMu.Lock()
	defer settledMu.Unlock()
	if settled["main-confirm"] == 0 || settled["worker-question"] == 0 {
		t.Fatalf("settled durations = %v, want both flows to have settled their waits", settled)
	}
}
