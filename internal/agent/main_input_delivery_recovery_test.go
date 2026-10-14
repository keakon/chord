package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

func TestInputDeliveryResumeRetiresUnknownInputs(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	state := &recovery.InputDeliveryState{Revision: 3,
		Gate:   recovery.FollowupGate{Mode: recovery.FollowupGatePaused, Reason: recovery.FollowupOutcomeUnknown, WaitingWorkID: "work"},
		Works:  []recovery.DeliveryWork{{WorkID: "work", Origin: recovery.DeliveryWorkUser, Outcome: recovery.DeliveryWorkUnknown}},
		Inputs: []recovery.PendingInput{{InputID: "input", WorkID: "work", Stage: recovery.PendingInputStageStarted}},
	}
	if err := a.applyDeliveryControl(&QueueControl{Action: InputQueueResume, ExpectedQueueRevision: 3}, state); err != nil {
		t.Fatal(err)
	}
	if len(state.Inputs) != 0 {
		t.Fatalf("resume retained %d consumed unknown inputs", len(state.Inputs))
	}
}

func TestInputDeliveryReconciledClaimRequiresStartPermit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.delivery.owner = a.currentRecoveryOwnership()
	state := &recovery.InputDeliveryState{Revision: 3,
		Gate:   recovery.FollowupGate{Mode: recovery.FollowupGateRunning, WaitingWorkID: work},
		Works:  []recovery.DeliveryWork{{WorkID: work, Origin: recovery.DeliveryWorkUser, Outcome: recovery.DeliveryWorkActive}},
		Inputs: []recovery.PendingInput{{InputID: "input", WorkID: work, Stage: recovery.PendingInputStageClaimed, DeliveryPolicy: recovery.InputDeliveryNextRequest}},
	}
	a.delivery.committed = recovery.ReconcileInputDelivery(state, []message.Message{{Role: message.RoleUser, InputID: "input", Content: "sample"}})
	if !a.prepareDeliveryRequest(context.Background(), a.turn.ID, "") {
		t.Fatal("request gate allows claimed input without durable start permit")
	}
}

func TestInputDeliveryPermitRecoveryBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{recovery.PendingInputStageClaimed, recovery.PendingInputStageStarted} {
		t.Run(stage, func(t *testing.T) {
			a, p := scenarioAgent(t, scriptedStreamCall{resp: &message.Response{Content: "sample response", StopReason: "stop"}})
			work := a.newDeliveryWork(recovery.DeliveryWorkUser)
			a.newTurn(work)
			id := submitDeliveryForTest(t, a, "sample", recovery.InputDeliveryNextRequest)
			a.delivery.pending = append(a.delivery.pending, &deliveryOperation{mutate: func(state *recovery.InputDeliveryState) error {
				state.Inputs[0].Stage = stage
				state.Inputs[0].WorkID = work
				state.Inputs[0].Revision++
				return nil
			}})
			a.startNextDeliveryOperation()
			finishDeliveryWrites(t, a)
			a.recordCommittedUserMessage(message.Message{Role: message.RoleUser, InputID: id, Content: "sample"})
			a.flushPersist()
			a.delivery.permit = a.newDeliveryPermit([]string{id}, work, a.WorkDirSnapshot(), a.turn.ID, a.turn.Epoch, a.delivery.cancelGeneration.Load())
			a.delivery.persistenceError = fmt.Errorf("sample permit write failure")
			a.delivery.request = &deliveryRequest{turnID: a.turn.ID, turnEpoch: a.turn.Epoch}
			runDeliveryOperation(t, a, &deliveryOperation{control: &QueueControl{OperationID: "pause", Target: a.InputDeliveryTarget(), Action: InputQueuePause, ExpectedQueueRevision: a.delivery.committed.Revision}})
			finishDeliveryWrites(t, a)
			waitForBlockingStreamProviderCalls(t, p, 1)
			disk, err := a.recoveryManager().LoadInputDelivery()
			if err != nil {
				t.Fatal(err)
			}
			if disk.Inputs[0].Stage != recovery.PendingInputStageStarted || disk.Works[0].Outcome != recovery.DeliveryWorkActive {
				t.Fatalf("request lacks active durable permit: %+v", disk)
			}
			if len(a.ctxMgr.Snapshot()) != 1 {
				t.Fatal("recovery appended input twice")
			}
		})
	}
}

func TestInputDeliveryCancelledPermitCannotResume(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	a.delivery.permit = a.newDeliveryPermit([]string{"input"}, work, a.WorkDirSnapshot(), a.turn.ID, a.turn.Epoch, a.delivery.cancelGeneration.Load())
	a.delivery.request = &deliveryRequest{turnID: a.turn.ID, turnEpoch: a.turn.Epoch}
	a.delivery.cancelGeneration.Add(1)
	if !a.retryDeliveryPermit() || a.delivery.request != nil || a.delivery.permit != nil || a.delivery.inflight != nil {
		t.Fatal("cancelled permit retried")
	}
}
