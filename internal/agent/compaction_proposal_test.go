package agent

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestModelDrivenProposalTransitionAllowedMatrix(t *testing.T) {
	terminal := []string{CompactionStatusSkipped, CompactionStatusFailed, CompactionStatusCancelled}
	tests := []struct {
		name string
		from string
		to   string
		want bool
	}{
		{name: "first accept from empty", to: modelDrivenProposalAccepted, want: true},
		{name: "re-arm after teardown", from: modelDrivenProposalAccepted, to: modelDrivenProposalAccepted, want: true},
		{name: "re-arm after settle", from: CompactionStatusSkipped, to: modelDrivenProposalAccepted, want: true},
		{name: "barrier from accepted", from: modelDrivenProposalAccepted, to: modelDrivenProposalPreparing, want: true},
		{name: "barrier without acceptance", to: modelDrivenProposalPreparing, want: false},
		{name: "apply after barrier", from: modelDrivenProposalPreparing, to: modelDrivenProposalApplied, want: true},
		{name: "apply without barrier", from: modelDrivenProposalAccepted, to: modelDrivenProposalApplied, want: false},
		{name: "skip after barrier", from: modelDrivenProposalPreparing, to: CompactionStatusSkipped, want: true},
		{name: "cancel armed request", from: modelDrivenProposalAccepted, to: CompactionStatusCancelled, want: true},
		{name: "terminal settle", from: modelDrivenProposalPreparing, to: CompactionStatusFailed, want: true},
	}
	for _, from := range append([]string{""}, terminal...) {
		for _, to := range terminal {
			tests = append(tests, struct {
				name string
				from string
				to   string
				want bool
			}{name: "terminal settle from " + from, from: from, to: to, want: from != modelDrivenProposalApplied})
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelDrivenProposalTransitionAllowed(tc.from, tc.to); got != tc.want {
				t.Fatalf("transition %q -> %q allowed = %v, want %v", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestModelDrivenProposalTerminalClearsAuditArgs(t *testing.T) {
	a := &MainAgent{}
	a.armModelDrivenProposal("call-1", tools.CompactContextArgs{ActiveObjective: "x"}, `{"active_objective":"x"}`, "accepted by runtime validation")
	if a.modelDrivenProposal.argsJSON == "" {
		t.Fatal("accepted proposal must retain its audit args")
	}
	// A skip settles the attempt: the args exist only while the proposal can
	// still apply, so the terminal settle clears them.
	a.transitionModelDrivenProposal(CompactionStatusSkipped, "projected savings too small")
	if a.modelDrivenProposal.argsJSON != "" {
		t.Fatalf("settled proposal args = %q, want cleared", a.modelDrivenProposal.argsJSON)
	}
	// Re-arming with a new request restores the audit copy and the identity.
	a.armModelDrivenProposal("call-2", tools.CompactContextArgs{ActiveObjective: "y"}, `{"active_objective":"y"}`, "accepted by runtime validation")
	if a.modelDrivenProposal.requestID != "call-2" || a.modelDrivenProposal.argsJSON == "" {
		t.Fatalf("re-armed proposal = %+v", a.modelDrivenProposal)
	}
}

func TestModelDrivenProposalAppliedIsTerminal(t *testing.T) {
	a := &MainAgent{}
	a.armModelDrivenProposal("call-1", tools.CompactContextArgs{}, `{"active_objective":"x"}`, "accepted")
	a.transitionModelDrivenProposal(modelDrivenProposalPreparing, "preparing")
	a.transitionModelDrivenProposal(modelDrivenProposalApplied, "applied")
	a.transitionModelDrivenProposal(CompactionStatusFailed, "late worker failure")
	if a.modelDrivenProposal.status != modelDrivenProposalApplied {
		t.Fatalf("applied proposal was overwritten by stale terminal status: %q", a.modelDrivenProposal.status)
	}
	if a.modelDrivenProposal.reason != "applied" {
		t.Fatalf("applied proposal reason was overwritten: %q", a.modelDrivenProposal.reason)
	}
}

func TestModelDrivenProposalTransitionPersistsSnapshotOnce(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	a.armModelDrivenProposal("call-1", tools.CompactContextArgs{ActiveObjective: "x"}, `{"active_objective":"x"}`, "accepted by runtime validation")
	// The transition updates status/reason/updatedAt as one step, and the
	// proposal identity is retained past the armed request's consumption.
	a.pendingModelDriven = nil
	a.transitionModelDrivenProposal(modelDrivenProposalPreparing, "preparing durable checkpoint")
	if a.modelDrivenProposal.status != modelDrivenProposalPreparing || a.modelDrivenProposal.reason != "preparing durable checkpoint" {
		t.Fatalf("proposal = %+v", a.modelDrivenProposal)
	}
	if a.modelDrivenProposal.requestID != "call-1" {
		t.Fatalf("proposal request id = %q, want call-1 (identity survives the barrier)", a.modelDrivenProposal.requestID)
	}
}
