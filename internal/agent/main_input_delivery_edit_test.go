package agent

import (
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// A follow-up queued with alt+enter can be pulled into the composer for
// editing. Sending that edit must release the pause derived from the hold,
// otherwise the item stays queued forever once its predecessor has settled.
func TestInputDeliveryEditedFollowupRunsAfterSettledPredecessor(t *testing.T) {
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
	// The predecessor completes while the follow-up is still being edited.
	a.settleDeliveryWork(work, recovery.DeliveryWorkCompleted)
	finishDeliveryWrites(t, a)
	if gate := a.delivery.committed.Gate; gate.Mode != recovery.FollowupGatePaused || gate.Reason != recovery.FollowupEditHold {
		t.Fatalf("edit hold did not pause the gate: %+v", gate)
	}
	edited := runDeliveryOperation(t, a, &deliveryOperation{update: &UpdateInput{OperationID: "edit", Target: a.InputDeliveryTarget(), InputID: followup, ExpectedRevision: held.ItemRevision, Content: new("edited followup")}})
	if edited.Result != InputQueueCommitted {
		t.Fatalf("edit: %+v", edited)
	}
	finishDeliveryWrites(t, a)
	state := a.delivery.committed
	if state.Gate.Mode != recovery.FollowupGateRunning || state.Gate.Reason != "" {
		t.Fatalf("sending the edit left the queue paused: %+v", state.Gate)
	}
	if ids := selectDeliveryInputs(state, true); len(ids) != 1 || ids[0] != followup {
		t.Fatalf("edited follow-up not runnable: %v", ids)
	}
}

// Releasing the hold must not release follow-ups behind a predecessor that did
// not complete: those keep waiting for an explicit queue resume.
func TestInputDeliveryEditedFollowupStaysPausedAfterFailure(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	followup := submitDeliveryForTest(t, a, "followup", recovery.InputDeliveryAfterCompletion)
	held := runDeliveryOperation(t, a, &deliveryOperation{control: &QueueControl{OperationID: "hold", Target: a.InputDeliveryTarget(), Action: InputQueueHoldEdit, InputID: followup, ExpectedRevision: 1}})
	if held.Result != InputQueueCommitted {
		t.Fatalf("hold: %+v", held)
	}
	a.settleDeliveryWork(work, recovery.DeliveryWorkFailed)
	finishDeliveryWrites(t, a)
	edited := runDeliveryOperation(t, a, &deliveryOperation{update: &UpdateInput{OperationID: "edit", Target: a.InputDeliveryTarget(), InputID: followup, ExpectedRevision: held.ItemRevision, Content: new("edited followup")}})
	if edited.Result != InputQueueCommitted {
		t.Fatalf("edit: %+v", edited)
	}
	finishDeliveryWrites(t, a)
	state := a.delivery.committed
	if state.Gate.Mode != recovery.FollowupGatePaused || state.Gate.Reason != recovery.FollowupPredecessorFailed {
		t.Fatalf("failed predecessor lost its protection: %+v", state.Gate)
	}
	if ids := selectDeliveryInputs(state, true); len(ids) != 0 {
		t.Fatalf("edited follow-up ran behind a failed predecessor: %v", ids)
	}
}
