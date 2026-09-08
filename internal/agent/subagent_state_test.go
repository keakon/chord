package agent

import (
	"math/rand"
	"testing"
)

func TestValidateSubAgentStateTransition(t *testing.T) {
	tests := []struct {
		name string
		from SubAgentState
		to   SubAgentState
		want bool
	}{
		{name: "initial running", to: SubAgentStateRunning, want: true},
		{name: "initial completed", to: SubAgentStateCompleted, want: true},
		{name: "initial empty rejected", to: SubAgentState("")},
		{name: "initial unknown rejected", to: SubAgentState("unknown-state")},
		{name: "waiting resumes", from: SubAgentStateWaitingMain, to: SubAgentStateRunning, want: true},
		{name: "waiting descendant completes", from: SubAgentStateWaitingDescendant, to: SubAgentStateCompleted, want: true},
		{name: "completed reactivates", from: SubAgentStateCompleted, to: SubAgentStateRunning, want: true},
		{name: "failed idle reset is not an ordinary transition", from: SubAgentStateFailed, to: SubAgentStateIdle, want: false},
		{name: "unknown destination rejected", from: SubAgentStateRunning, to: SubAgentState("unknown")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validSubAgentStateTransition(test.from, test.to); got != test.want {
				t.Fatalf("validSubAgentStateTransition(%q, %q) = %v, want %v", test.from, test.to, got, test.want)
			}
		})
	}
}

func TestSubAgentStateSetRejectsUnknownInitialState(t *testing.T) {
	var state subAgentRuntimeState
	if ok := state.set(SubAgentState("unknown-state"), "invalid"); ok {
		t.Fatal("unknown initial state reported success")
	}
	if got, _ := state.snapshot(); got != "" {
		t.Fatalf("state after rejected unknown initialization = %q, want empty", got)
	}
}

func TestSubAgentRuntimeStateSetAllowsExplicitTerminalReactivation(t *testing.T) {
	var state subAgentRuntimeState
	state.set(SubAgentStateCompleted, "done")
	state.set(SubAgentStateRunning, "follow-up")
	got, summary := state.snapshot()
	if got != SubAgentStateRunning || summary != "follow-up" {
		t.Fatalf("state after terminal reactivation = (%q, %q), want (running, follow-up)", got, summary)
	}
}

func TestSubAgentRuntimeStateSetInvalidTransitionReturnsFalse(t *testing.T) {
	var state subAgentRuntimeState
	state.set(SubAgentStateCompleted, "done")
	if ok := state.set(SubAgentStateWaitingMain, "regress"); ok {
		t.Fatal("invalid terminal transition reported success")
	}
	got, summary := state.snapshot()
	if got != SubAgentStateCompleted || summary != "done" {
		t.Fatalf("state after rejected transition = (%q, %q), want (completed, done)", got, summary)
	}
}

func TestSubAgentRuntimeStateResetForAttemptRequiresTerminalState(t *testing.T) {
	var state subAgentRuntimeState
	state.set(SubAgentStateCompleted, "done")
	if !state.resetForAttempt("new attempt") {
		t.Fatal("terminal runtime was not reset for a new attempt")
	}
	got, summary := state.snapshot()
	if got != SubAgentStateIdle || summary != "new attempt" {
		t.Fatalf("reset state = (%q, %q), want (idle, new attempt)", got, summary)
	}
	if state.resetForAttempt("invalid second reset") {
		t.Fatal("non-terminal runtime reset unexpectedly succeeded")
	}
}

func TestSubAgentRejectedStateTransitionIsRecorded(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := &SubAgent{parent: a, instanceID: "agent-1"}
	sub.setState(SubAgentStateCompleted, "done")
	if got := a.OrchestrationStats().StateTransitionsRejected; got != 0 {
		t.Fatalf("legal transitions recorded as rejected: %d", got)
	}
	sub.setState(SubAgentStateWaitingMain, "illegal regress")
	if got := a.OrchestrationStats().StateTransitionsRejected; got != 1 {
		t.Fatalf("rejected transition count = %d, want 1", got)
	}
	if got := a.OrchestrationStats().StateTransitionRejections["terminal"]; got != 1 {
		t.Fatalf("terminal rejection count = %d, want 1", got)
	}
	if got := sub.State(); got != SubAgentStateCompleted {
		t.Fatalf("state after rejected transition = %q, want completed", got)
	}
}

func TestSubAgentStateMachineRandomWalkInvariants(t *testing.T) {
	states := []SubAgentState{
		"", SubAgentStateRunning, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant,
		SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled, SubAgentStateIdle,
		SubAgentState("unknown-state"),
	}
	for seed := int64(0); seed < 50; seed++ {
		rng := rand.New(rand.NewSource(seed))
		var state subAgentRuntimeState
		state.set(SubAgentStateRunning, "init")
		for step := 0; step < 500; step++ {
			from, previousSummary := state.snapshot()
			to := states[rng.Intn(len(states))]
			beforeChangedAt := state.stateChangedAt
			applied := state.set(to, "step")
			want := validSubAgentStateTransition(from, to)
			if applied != want {
				t.Fatalf("seed=%d step=%d set(%q -> %q) applied=%v, spec says %v", seed, step, from, to, applied, want)
			}
			got, gotSummary := state.snapshot()
			if applied {
				if got != to {
					t.Fatalf("seed=%d step=%d applied transition left state=%q, want %q", seed, step, got, to)
				}
			} else {
				if got != from {
					t.Fatalf("seed=%d step=%d rejected transition changed state to %q, want %q", seed, step, got, from)
				}
				if gotSummary != previousSummary {
					t.Fatalf("seed=%d step=%d rejected transition changed summary to %q, want %q", seed, step, gotSummary, previousSummary)
				}
				if state.stateChangedAt != beforeChangedAt {
					t.Fatalf("seed=%d step=%d rejected transition refreshed activity timestamp", seed, step)
				}
			}
			if got == "" {
				t.Fatalf("seed=%d step=%d state became empty", seed, step)
			}
		}
	}
}
