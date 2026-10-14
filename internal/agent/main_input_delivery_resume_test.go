package agent

import (
	"fmt"
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func TestInputDeliveryQueueResumeAfterStop(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	work := a.newDeliveryWork(recovery.DeliveryWorkUser)
	a.newTurn(work)
	submitDeliveryForTest(t, a, "sample followup", recovery.InputDeliveryAfterCompletion)
	a.suspendPendingUserDrain()
	a.settleDeliveryWork(work, recovery.DeliveryWorkCancelled)
	finishDeliveryWrites(t, a)
	a.turn = nil
	result := runDeliveryOperation(t, a, &deliveryOperation{control: &QueueControl{OperationID: "resume", Target: a.InputDeliveryTarget(), Action: InputQueueResume, ExpectedQueueRevision: a.delivery.committed.Revision}})
	finishDeliveryWrites(t, a)
	if result.Result != InputQueueCommitted {
		t.Fatalf("resume failed: %+v", result)
	}
	if a.pendingUserDrainSuspended {
		t.Fatal("resume committed but scheduler remains suspended")
	}
	// Keep the test at the durable start boundary without dispatching a model.
	a.delivery.request = &deliveryRequest{turnID: 1000}
	a.advanceInputDelivery()
	finishDeliveryWrites(t, a)
	messages := a.ctxMgr.Snapshot()
	if len(messages) != 1 || messages[0].Content != "sample followup" {
		t.Fatalf("resumed input did not reach the transcript: %+v", messages)
	}
	state, err := a.recoveryManager().LoadInputDelivery()
	if err != nil || len(state.Inputs) != 1 || state.Inputs[0].Stage != recovery.PendingInputStageStarted {
		t.Fatalf("resumed input lacks durable start: %+v %v", state, err)
	}
}

func TestInputDeliveryResumePreservesNewBlockers(t *testing.T) {
	t.Parallel()
	for _, blocker := range []string{"cancel", "session_pause", "write_error"} {
		t.Run(blocker, func(t *testing.T) {
			a := newTestMainAgent(t, t.TempDir())
			a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
			submitDeliveryForTest(t, a, "sample", recovery.InputDeliveryAfterCompletion)
			a.turn = nil
			a.suspendPendingUserDrain()
			op := &deliveryOperation{control: &QueueControl{OperationID: "resume", Target: a.InputDeliveryTarget(), Action: InputQueueResume, ExpectedQueueRevision: a.delivery.committed.Revision}}
			a.handleDeliveryOperation(op)
			if !a.pendingUserDrainSuspended {
				t.Fatal("resumed before durable commit")
			}
			switch blocker {
			case "cancel":
				a.delivery.cancelGeneration.Add(1)
			case "session_pause":
				a.sessionExecutionGate().TryFreeze(1)
			}
			for a.delivery.inflight != nil {
				select {
				case evt := <-a.eventCh:
					if evt.Type != eventInputDeliveryCommitted {
						continue
					}
					if blocker == "write_error" {
						commit := evt.Payload.(deliveryCommit)
						commit.err = fmt.Errorf("sample storage failure")
						evt.Payload = commit
					}
					a.dispatch(evt)
				case <-time.After(10 * time.Second):
					t.Fatal("commit timed out")
				}
			}
			if !a.pendingUserDrainSuspended {
				t.Fatal("resume bypassed blocker")
			}
		})
	}
}
