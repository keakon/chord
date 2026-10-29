package agent

import (
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// Two follow-ups queued behind the same work run one after another: when the
// first one fails, the second waits for the user's next request instead of
// running behind the failure.
func TestInputDeliveryChainedFollowupWaitsBehindFailedFollowup(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial task"})
	first := submitDeliveryForTest(t, a, "first", recovery.InputDeliveryAfterCompletion)
	second := submitDeliveryForTest(t, a, "second", recovery.InputDeliveryAfterCompletion)
	a.settleDeliveryWork(work, recovery.DeliveryWorkCompleted)
	finishDeliveryWrites(t, a)
	ids := selectDeliveryInputs(a.delivery.committed, true)
	if len(ids) != 1 || ids[0] != first {
		t.Fatalf("first selection: %v", ids)
	}
	a.delivery.request = &deliveryRequest{turnID: a.turn.ID + 100}
	a.claimDeliveryInputs(ids, true)
	finishDeliveryWrites(t, a)
	firstWork := a.turn.DeliveryWorkID
	for _, input := range a.delivery.committed.Inputs {
		if input.InputID == second && input.WaitWorkID != firstWork {
			t.Fatalf("second follow-up waits on %q, want the running first follow-up %q", input.WaitWorkID, firstWork)
		}
	}
	a.settleDeliveryWork(firstWork, recovery.DeliveryWorkFailed)
	finishDeliveryWrites(t, a)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("second follow-up ran behind a failed follow-up: %v", ids)
	}
}

// Editing only the content of a follow-up held behind a failed predecessor
// must not start it.
func TestInputDeliveryContentEditKeepsWait(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial task"})
	followup := submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	a.settleDeliveryWork(work, recovery.DeliveryWorkFailed)
	finishDeliveryWrites(t, a)
	edited := runDeliveryOperation(t, a, &deliveryOperation{update: &UpdateInput{OperationID: "edit", Target: a.InputDeliveryTarget(), InputID: followup, ExpectedRevision: 1, Content: new("edited followup"), DeliveryPolicy: recovery.InputDeliveryAfterCompletion}})
	if edited.Result != InputQueueCommitted {
		t.Fatalf("edit: %+v", edited)
	}
	finishDeliveryWrites(t, a)
	if got := a.delivery.committed.Inputs[0].WaitWorkID; got != work {
		t.Fatalf("content edit re-set the wait to %q, want %q", got, work)
	}
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("content edit started a held follow-up: %v", ids)
	}
}

// A session that already has a durable queue must still switch sessions
// (/new, /resume, fork) once the queue settles, without writing the queue.
func TestInputDeliverySessionSwitchWithDurableQueueResumes(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial"})
	submitDeliveryForTest(t, a, "queued", recovery.InputDeliveryNextRequest)
	finishDeliveryWrites(t, a)
	if a.delivery.committed == nil {
		t.Fatal("expected a durable queue state")
	}
	revision := a.delivery.committed.Revision
	resumed := false
	if !a.deferDeliverySessionSwitch(func() { resumed = true }, func() {}) {
		t.Fatal("expected the switch to be deferred behind the queue")
	}
	finishDeliveryWrites(t, a)
	if !resumed {
		t.Fatalf("session switch never resumed: navigation=%v", a.delivery.navigation)
	}
	if got := a.delivery.committed.Revision; got != revision {
		t.Fatalf("switch barrier wrote the queue: revision %d -> %d", revision, got)
	}
}

// An unclean exit leaves a queued input in the durable queue. The restored
// session starts nothing on its own; the user's next message releases it.
func TestInputDeliveryRestoredQueueWaitsForUser(t *testing.T) {
	projectRoot := t.TempDir()
	sessionDir := testProjectSessionDir(t, projectRoot, "unclean-restore-queued")
	persistRestorableSession(t, sessionDir)
	a := newTestMainAgentForRestore(t, projectRoot, sessionDir)
	dir := a.WorkDirSnapshot()
	rm := recovery.NewRecoveryManager(sessionDir)
	if _, err := rm.SaveInputDelivery(0, &recovery.InputDeliveryState{
		SessionID: filepath.Base(sessionDir), Revision: 1, NextSequence: 1,
		Inputs: []recovery.PendingInput{{
			InputID: "queued-1", OwnerTaskID: identity.MainAgentID, Sequence: 1, Revision: 1,
			Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryNextRequest,
			WorkDir: dir.Path, WorktreeID: dir.WorktreeID, Content: "queued follow-up", FromUser: true,
		}},
	}); err != nil {
		t.Fatalf("seed queue: %v", err)
	}
	if err := rm.Close(); err != nil {
		t.Fatalf("close seed manager: %v", err)
	}
	if err := a.RestoreSessionAtStartup(); err != nil {
		t.Fatalf("RestoreSessionAtStartup: %v", err)
	}
	a.initializeInputDelivery()
	finishDeliveryWrites(t, a)
	a.advanceInputDelivery()
	finishDeliveryWrites(t, a)
	if a.turn != nil || a.delivery.committed.Inputs[0].Stage != recovery.PendingInputStageQueued {
		t.Fatalf("restored input started without user action: stage=%q turn=%v", a.delivery.committed.Inputs[0].Stage, a.turn != nil)
	}
	if !a.mailboxDeliveryPaused.Load() {
		t.Fatal("restore released the mailbox barrier without user action")
	}
	if snapshot := a.InputQueueSnapshot(); snapshot == nil || !snapshot.RestoredHeld {
		t.Fatalf("snapshot does not report the restore hold: %+v", snapshot)
	}
	if a.deliveryPreventsGlobalIdle() {
		t.Fatal("held restored input must not keep the agent busy")
	}
	a.releaseRestoredHold()
	if ids := a.runnableDeliveryInputs(true); len(ids) != 1 || ids[0] != "queued-1" {
		t.Fatalf("released hold did not make the restored input runnable: %v", ids)
	}
}

func seedRestoredDeliveryQueue(t *testing.T, a *MainAgent) string {
	t.Helper()
	dir := a.WorkDirSnapshot()
	inputID := "restored-queue"
	state, err := a.recoveryManager().SaveInputDelivery(0, &recovery.InputDeliveryState{
		SessionID: filepath.Base(a.sessionDir), Revision: 1, NextSequence: 1,
		Inputs: []recovery.PendingInput{{
			InputID: inputID, OwnerTaskID: identity.MainAgentID, Sequence: 1, Revision: 1,
			Stage: recovery.PendingInputStageQueued, DeliveryPolicy: recovery.InputDeliveryNextRequest,
			WorkDir: dir.Path, WorktreeID: dir.WorktreeID, Content: "restored input", FromUser: true,
		}},
	})
	if err != nil {
		t.Fatalf("seed restored queue: %v", err)
	}
	a.delivery.committed = state
	a.delivery.owner = a.currentRecoveryOwnership()
	a.delivery.restoredHeld = true
	a.mailboxDeliveryPaused.Store(true)
	a.publishInputDelivery()
	return inputID
}

func TestInputDeliveryRestoredQueueWaitsForContinue(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	seedRestoredDeliveryQueue(t, a)

	a.dispatch(Event{Type: EventContinue})

	if a.delivery.restoredHeld {
		t.Fatal("Continue did not release the restored input hold")
	}
	if a.mailboxDeliveryPaused.Load() {
		t.Fatal("Continue did not release the restored mailbox hold")
	}
	if a.turn == nil {
		t.Fatal("Continue did not start a main turn")
	}
}

func TestInputDeliveryRestoredQueueWaitsForNewUserMessage(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	seedRestoredDeliveryQueue(t, a)

	a.handleUserMessage(Event{Type: EventUserMessage, Payload: "continue with the new request"})

	if a.delivery.restoredHeld {
		t.Fatal("new user message did not release the restored input hold")
	}
	if a.mailboxDeliveryPaused.Load() {
		t.Fatal("new user message did not release the restored mailbox hold")
	}
	finishDeliveryWrites(t, a)
	if got := len(a.delivery.committed.Inputs); got != 2 {
		t.Fatalf("durable queue has %d inputs after the new message, want 2", got)
	}
}

func TestInputDeliveryQueueEditsKeepRestoredHold(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	inputID := seedRestoredDeliveryQueue(t, a)

	edited := runDeliveryOperation(t, a, &deliveryOperation{update: &UpdateInput{
		OperationID: "edit-restored", Target: a.InputDeliveryTarget(), InputID: inputID,
		ExpectedRevision: 1, Content: new("edited restored input"),
	}})
	if edited.Result != InputQueueCommitted {
		t.Fatalf("edit: %+v", edited)
	}
	if !a.delivery.restoredHeld || !a.mailboxDeliveryPaused.Load() {
		t.Fatal("editing a restored queue entry released a user-action hold")
	}
	if ids := a.runnableDeliveryInputs(true); len(ids) != 0 {
		t.Fatalf("edited restored input became runnable without user action: %v", ids)
	}

	deleted := runDeliveryOperation(t, a, &deliveryOperation{delete: &DeleteInput{
		OperationID: "delete-restored", Target: a.InputDeliveryTarget(), InputID: inputID,
		ExpectedRevision: 2,
	}})
	if deleted.Result != InputQueueCommitted {
		t.Fatalf("delete: %+v", deleted)
	}
	if !a.delivery.restoredHeld || !a.mailboxDeliveryPaused.Load() {
		t.Fatal("deleting a restored queue entry released the mailbox hold")
	}
}

// A started row left by an interrupted work exists so an explicit continue can
// adopt it. A new turn that did not adopt it supersedes that work, so the row
// must not linger and count against the queue limit.
func TestInputDeliveryNewTurnRetiresRowsOfUnknownWork(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	submitDeliveryForTest(t, a, "queued", recovery.InputDeliveryNextRequest)
	finishDeliveryWrites(t, a)
	state := a.delivery.committed.Clone()
	state.Inputs[0].Stage, state.Inputs[0].WorkID = recovery.PendingInputStageStarted, "dead-work"
	state.Works = append(state.Works, recovery.DeliveryWork{WorkID: "dead-work", Origin: recovery.DeliveryWorkUser, Outcome: recovery.DeliveryWorkUnknown})
	a.delivery.committed = state
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	finishDeliveryWrites(t, a)
	if n := len(a.delivery.committed.Inputs); n != 0 {
		t.Fatalf("new turn kept %d row(s) of the superseded unknown work: %+v", n, a.delivery.committed.Inputs)
	}
}
