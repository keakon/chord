package agent

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/recovery"
)

func runDeliveryOperation(t *testing.T, a *MainAgent, op *deliveryOperation) InputQueueOperationResultEvent {
	t.Helper()
	a.handleDeliveryOperation(op)
	id, _ := op.identity()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-a.eventCh:
			a.dispatch(event)
		case event := <-a.outputCh:
			if result, ok := event.(InputQueueOperationResultEvent); ok && result.OperationID == id {
				return result
			}
		case <-deadline.C:
			t.Fatal("queue operation did not settle")
			return InputQueueOperationResultEvent{}
		}
	}
}

func TestInputDeliveryDurableSubmitAndEdit(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	a.newTurn(a.newDeliveryWork(recovery.DeliveryWorkUser))
	target := a.InputDeliveryTarget()
	result := runDeliveryOperation(t, a, &deliveryOperation{submit: &SubmitInput{OperationID: "submit-1", Target: target, DeliveryPolicy: recovery.InputDeliveryAfterCompletion, Content: "sample followup"}})
	if result.Result != InputQueueCommitted || result.InputID == "" || result.QueueRevision != 1 {
		t.Fatalf("submit: %+v", result)
	}
	if len(a.ctxMgr.Snapshot()) != 0 {
		t.Fatal("queued input leaked into context")
	}
	loaded, err := a.recoveryManager().LoadInputDelivery()
	if err != nil || len(loaded.Inputs) != 1 || loaded.Inputs[0].InputID != result.InputID {
		t.Fatalf("durable input: %+v %v", loaded, err)
	}
	held := runDeliveryOperation(t, a, &deliveryOperation{control: &QueueControl{OperationID: "hold-1", Target: target, Action: InputQueueHoldEdit, InputID: result.InputID, ExpectedRevision: 1}})
	if held.Result != InputQueueCommitted || held.ItemRevision != 2 {
		t.Fatalf("hold: %+v", held)
	}
	update := &UpdateInput{OperationID: "edit-1", Target: target, InputID: result.InputID, ExpectedRevision: 1, Content: new("replacement")}
	conflict := runDeliveryOperation(t, a, &deliveryOperation{update: update})
	if conflict.Result != InputQueueConflict {
		t.Fatalf("stale edit: %+v", conflict)
	}
	update.OperationID = "edit-2"
	update.ExpectedRevision = 2
	edited := runDeliveryOperation(t, a, &deliveryOperation{update: update})
	if edited.Result != InputQueueCommitted || edited.ItemRevision != 3 {
		t.Fatalf("edit: %+v", edited)
	}
	snapshot := a.InputQueueSnapshot()
	if snapshot.State.Inputs[0].Content != "replacement" || snapshot.State.Gate.Mode != recovery.FollowupGateRunning || len(snapshot.State.EditHolds) != 0 {
		t.Fatalf("edited snapshot: %+v", snapshot)
	}
	// Releasing the hold restores the interrupted gate, but the follow-up still
	// waits for the work it was queued behind.
	if ids := selectDeliveryInputs(snapshot.State, true); len(ids) != 0 {
		t.Fatalf("edited follow-up ran before its predecessor settled: %v", ids)
	}
	snapshot.State.Inputs[0].Content = "mutated copy"
	if a.InputQueueSnapshot().State.Inputs[0].Content != "replacement" {
		t.Fatal("snapshot aliases runtime state")
	}
}

func TestInputDeliveryRejectsStaleTarget(t *testing.T) {
	t.Parallel()
	a := newTestMainAgent(t, t.TempDir())
	target := a.InputDeliveryTarget()
	target.SessionID = "other-session"
	result := runDeliveryOperation(t, a, &deliveryOperation{submit: &SubmitInput{OperationID: "submit-1", Target: target, DeliveryPolicy: recovery.InputDeliveryAfterCompletion, Content: "sample"}})
	if result.Result != InputQueueRejected {
		t.Fatalf("stale target: %+v", result)
	}
	if state, err := a.recoveryManager().LoadInputDelivery(); err != nil || state != nil {
		t.Fatalf("rejected input persisted: %+v %v", state, err)
	}
}

func TestInputDeliveryFinalResponse(t *testing.T) {
	for _, reason := range []string{"stop", "end_turn", "stop_sequence", "STOP"} {
		if !isDeliveryFinalResponse(&LLMResponsePayload{Content: "sample result", StopReason: reason}) {
			t.Fatalf("valid stop rejected: %s", reason)
		}
	}
	for _, reason := range []string{"", "length", "max_tokens", "MAX_TOKENS", "interrupted", "pause_turn", "content_filter", "tool_calls", "unknown"} {
		if isDeliveryFinalResponse(&LLMResponsePayload{Content: "sample result", StopReason: reason}) {
			t.Fatalf("invalid stop accepted: %s", reason)
		}
	}
}
