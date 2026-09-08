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
