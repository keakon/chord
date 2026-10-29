package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// A follow-up queued behind running work becomes runnable as soon as that work
// settles successfully — no edit or resume command involved.
func TestInputDeliveryFollowupRunsAfterSettledPredecessor(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial task"})
	followup := submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	finishDeliveryWrites(t, a)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("follow-up ran while its predecessor was still active: %v", ids)
	}
	a.settleDeliveryWork(work, recovery.DeliveryWorkCompleted)
	finishDeliveryWrites(t, a)
	state := a.delivery.committed
	if ids := selectDeliveryInputs(state, true); len(ids) != 1 || ids[0] != followup {
		t.Fatalf("follow-up not runnable after its predecessor settled: %v", ids)
	}
}

// Switching an edited follow-up to next-request clears its wait: it is an
// ordinary input again even though the predecessor it was queued behind
// failed.
func TestInputDeliveryEditResetsWaitBaseline(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial task"})
	followup := submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	held := runDeliveryOperation(t, a, &deliveryOperation{control: &QueueControl{OperationID: "hold", Target: a.InputDeliveryTarget(), Action: InputQueueHoldEdit, InputID: followup, ExpectedRevision: 1}})
	if held.Result != InputQueueCommitted {
		t.Fatalf("hold: %+v", held)
	}
	a.settleDeliveryWork(work, recovery.DeliveryWorkFailed)
	finishDeliveryWrites(t, a)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("follow-up ran behind a failed predecessor: %v", ids)
	}
	edited := runDeliveryOperation(t, a, &deliveryOperation{update: &UpdateInput{OperationID: "edit", Target: a.InputDeliveryTarget(), InputID: followup, ExpectedRevision: held.ItemRevision, Content: new("edited followup"), DeliveryPolicy: recovery.InputDeliveryNextRequest}})
	if edited.Result != InputQueueCommitted {
		t.Fatalf("edit: %+v", edited)
	}
	finishDeliveryWrites(t, a)
	state := a.delivery.committed
	if state.Inputs[0].DeliveryPolicy != recovery.InputDeliveryNextRequest || state.Inputs[0].WaitWorkID != "" {
		t.Fatalf("edit did not re-set the queue entry: %+v", state.Inputs[0])
	}
	if ids := selectDeliveryInputs(state, false); len(ids) != 1 || ids[0] != followup {
		t.Fatalf("edited input not runnable as an ordinary request: %v", ids)
	}
}

// A turn replaced while still running never settles its work: the wait pinned
// to it must re-point at the replacing turn, or the follow-up would read
// "task in progress" forever.
func TestInputDeliveryReplacedWorkStillUnlocks(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, Content: "initial task"})
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	followup := submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	finishDeliveryWrites(t, a)
	// Replace the turn without settling its work, the way interrupt paths do.
	replacement := a.newDeliveryWork(recovery.DeliveryWorkContext)
	a.newTurn(replacement)
	finishDeliveryWrites(t, a)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 0 {
		t.Fatalf("followup ran while the replacement was still active: %v", ids)
	}
	a.settleDeliveryWork(replacement, recovery.DeliveryWorkCompleted)
	finishDeliveryWrites(t, a)
	if ids := selectDeliveryInputs(a.delivery.committed, true); len(ids) != 1 || ids[0] != followup {
		t.Fatalf("followup not unlocked by the replacement settle: %v", ids)
	}
}
