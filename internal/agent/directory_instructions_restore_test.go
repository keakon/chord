package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/recovery"
)

func TestDirectoryInstructionRestoreBudgetKeepsInitialAndBlocksChangedDynamic(t *testing.T) {
	initial := directoryTestFact(".", "mandatory root rules")
	old := directoryTestFact("pkg/deep", "stale rules")
	changed := directoryTestFact("pkg/deep", "current rules")
	ancestor := directoryTestFact("pkg", "new ancestor rules")
	r := newDirectoryInstructionRuntime(t.Context(), directoryInstructionBinding{CheckoutRoot: "/repo"})
	defer r.cancel()
	r.initial = directoryInstructionSnapshot{Observations: map[string]directoryInstructionObservation{".": {Status: "present", SourcePath: initial.SourcePath, Body: initial.Body, BodyHash: initial.BodyHash}}}
	history := []message.Message{directoryInstructionFactMessage([]message.DirectoryInstructionFact{old})}
	facts, err := budgetRestoredDirectoryInstructions(r, []message.DirectoryInstructionFact{initial, changed, ancestor}, history, directoryCandidateBudgetInputs{budget: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 3 {
		t.Fatalf("facts: %+v", facts)
	}
	for _, f := range facts {
		if f.Scope == "." {
			if f != initial {
				t.Fatal("mandatory initial chain was downgraded")
			}
		} else if f.Operation != message.DirectoryInstructionBlocked || f.Reason != message.DirectoryInstructionReasonInputBudget {
			t.Fatalf("dynamic candidate escaped budget: %+v", f)
		}
	}
	v := buildDirectoryInstructionView(r.initial, append(history, directoryInstructionFactMessage(facts)), r.binding)
	if v.Err != nil || len(v.Dynamic) != 0 || len(v.Initial) != 1 || len(v.Blocked) != 2 {
		t.Fatalf("stale dynamic body survived restore: %+v", v)
	}
}

func TestDirectoryInstructionClearTurnDiscoveryRetainsFrozenRules(t *testing.T) {
	r := newDirectoryInstructionRuntime(t.Context(), directoryInstructionBinding{})
	defer r.cancel()
	r.pending = map[string]directoryInstructionObservation{"pkg": {Status: "present"}}
	previous := &directoryInstructionAttempt{gate: &directoryInstructionGate{}}
	r.attempt.Store(previous)
	frozen := r.frozen.Load()
	r.clearTurnDiscovery()
	if len(r.pending) != 0 || r.attempt.Load() != nil || previous.gate.TryAdmit("late") {
		t.Fatal("turn discovery remained live")
	}
	if r.frozen.Load() != frozen || r.ctx.Err() != nil {
		t.Fatal("turn cleanup discarded binding snapshot")
	}
}

func TestDirectoryInstructionEmptyPermissionsFollowToolDefault(t *testing.T) {
	check, _ := directoryInstructionPermission(nil, permission.PathScope{Cwd: "/repo"}, directoryInstructionPermissionMode{})
	if err := check(t.Context(), "/repo/AGENTS.md"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if check(ctx, "/repo/AGENTS.md") == nil {
		t.Fatal("cancelled discovery allowed")
	}
}

func TestDirectoryInstructionCancelledPersistenceFailureRetainsCandidateForRetry(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	fact := directoryTestFact("pkg", "durable rules")
	msg := directoryInstructionFactMessage([]message.DirectoryInstructionFact{fact})
	p := &directoryInstructionPreparation{candidate: &msg, binding: directoryInstructionBinding{SessionEpoch: a.sessionEpoch}, turnID: 10}
	a.directoryPreparation = p
	manager := a.recoveryManager()
	cause := &recovery.PersistWriteError{RetrySafe: true, Cause: errors.New("temporarily unavailable")}
	a.persist.rememberFailure(persistEntry{recovery: manager, agentID: identity.MainAgentID, msg: msg, durable: true}, cause)
	a.handleDirectoryInstructionsPrepared(Event{Payload: directoryInstructionsPrepared{preparation: p, facts: msg.DirectoryInstructions.Facts, committed: true, err: cause}})
	if a.directoryPreparation != p || !p.failed || p.adopted {
		t.Fatal("failed accepted candidate was forgotten")
	}
	if a.reconcileDirectoryInstructionsAfterFlush() == nil {
		t.Fatal("failed append was adopted before retry")
	}
	if err := a.persist.retryFailures(manager); err != nil {
		t.Fatal(err)
	}
	before := len(a.ctxMgr.Snapshot())
	if err := a.reconcileDirectoryInstructionsAfterFlush(); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileDirectoryInstructionsAfterFlush(); err != nil {
		t.Fatal(err)
	}
	if !p.adopted || len(a.ctxMgr.Snapshot()) != before+1 {
		t.Fatal("retry did not adopt exactly one durable fact")
	}
}

func TestDirectoryInstructionCancelledAckPublishesRecoveredInitialScope(t *testing.T) {
	a := newTestMainAgent(t, t.TempDir())
	binding := directoryInstructionBinding{CheckoutRoot: "/repo", SessionEpoch: a.sessionEpoch}
	r := newDirectoryInstructionRuntime(t.Context(), binding)
	defer r.cancel()
	r.initial = directoryInstructionSnapshot{Observations: map[string]directoryInstructionObservation{".": {Status: "blocked", Reason: "io"}}}
	a.directoryInstructions.Store(r)
	fact := directoryTestFact(".", "recovered root rules")
	msg := directoryInstructionFactMessage([]message.DirectoryInstructionFact{fact})
	p := &directoryInstructionPreparation{candidate: &msg, binding: binding, turnID: 10, dynamic: true}
	a.directoryPreparation = p
	a.handleDirectoryInstructionsPrepared(Event{Payload: directoryInstructionsPrepared{preparation: p, runtime: r, facts: msg.DirectoryInstructions.Facts, committed: true}})
	v := r.view.Load()
	if !p.adopted || v == nil || len(v.Initial) != 1 || v.Initial[0].Body != fact.Body || len(v.Blocked) != 0 {
		t.Fatalf("cancelled ack did not publish durable initial recovery: %+v", v)
	}
}
