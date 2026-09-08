package agent

import "testing"

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
		{name: "waiting resumes", from: SubAgentStateWaitingMain, to: SubAgentStateRunning, want: true},
		{name: "waiting descendant completes", from: SubAgentStateWaitingDescendant, to: SubAgentStateCompleted, want: true},
		{name: "completed reactivates", from: SubAgentStateCompleted, to: SubAgentStateRunning, want: true},
		{name: "failed cannot become idle", from: SubAgentStateFailed, to: SubAgentStateIdle},
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
	if ok := state.set(SubAgentStateIdle, "regress"); ok {
		t.Fatal("invalid terminal transition reported success")
	}
	got, summary := state.snapshot()
	if got != SubAgentStateCompleted || summary != "done" {
		t.Fatalf("state after rejected transition = (%q, %q), want (completed, done)", got, summary)
	}
}

func TestSubAgentRejectedStateTransitionIsRecorded(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	sub := &SubAgent{parent: a, instanceID: "agent-1"}
	sub.setState(SubAgentStateCompleted, "done")
	if got := a.OrchestrationStats().StateTransitionsRejected; got != 0 {
		t.Fatalf("legal transitions recorded as rejected: %d", got)
	}
	sub.setState(SubAgentStateIdle, "illegal regress")
	if got := a.OrchestrationStats().StateTransitionsRejected; got != 1 {
		t.Fatalf("rejected transition count = %d, want 1", got)
	}
	if got := sub.State(); got != SubAgentStateCompleted {
		t.Fatalf("state after rejected transition = %q, want completed", got)
	}
}
