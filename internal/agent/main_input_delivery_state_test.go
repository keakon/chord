package agent

import (
	"reflect"
	"testing"

	"github.com/keakon/chord/internal/recovery"
)

func TestInputDeliverySelection(t *testing.T) {
	queued := func(id string, seq int64, policy recovery.InputDeliveryPolicy) recovery.PendingInput {
		return recovery.PendingInput{InputID: id, Sequence: seq, Stage: recovery.PendingInputStageQueued, DeliveryPolicy: policy}
	}
	waiting := func(id string, seq int64) recovery.PendingInput {
		input := queued(id, seq, recovery.InputDeliveryAfterCompletion)
		input.WaitWorkID = "work-a"
		return input
	}
	state := &recovery.InputDeliveryState{Works: []recovery.DeliveryWork{{WorkID: "work-a", Outcome: recovery.DeliveryWorkActive}},
		Inputs: []recovery.PendingInput{waiting("b", 1), queued("d", 2, recovery.InputDeliveryNextRequest), waiting("c", 3)},
	}
	if got := selectDeliveryInputs(state, true); !reflect.DeepEqual(got, []string{"d"}) {
		t.Fatalf("ordinary priority: %v", got)
	}
	state.EditHolds = []string{"d"}
	if got := selectDeliveryInputs(state, true); len(got) != 0 {
		t.Fatalf("hold bypassed: %v", got)
	}
	state.EditHolds = nil
	state.Inputs = append(state.Inputs[:1], state.Inputs[2:]...)
	if got := selectDeliveryInputs(state, true); len(got) != 0 {
		t.Fatalf("active work bypassed: %v", got)
	}
	state.Works[0].Outcome = recovery.DeliveryWorkCompleted
	if got := selectDeliveryInputs(state, true); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("serial followup: %v", got)
	}
	// A follow-up queued while nothing ran never inherits an older failure.
	state.Inputs[0].WaitWorkID = ""
	if got := selectDeliveryInputs(state, true); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("idle-deposit followup blocked: %v", got)
	}
}

func TestInputDeliveryEditCAS(t *testing.T) {
	state := &recovery.InputDeliveryState{Inputs: []recovery.PendingInput{{InputID: "input-1", Revision: 2, Stage: recovery.PendingInputStageQueued}}}
	if _, err := deliveryInputForEdit(state, "input-1", 1); err == nil {
		t.Fatal("stale edit accepted")
	}
	if _, err := deliveryInputForEdit(state, "input-1", 2); err != nil {
		t.Fatal(err)
	}
	state.Inputs[0].Stage = recovery.PendingInputStageClaimed
	if _, err := deliveryInputForEdit(state, "input-1", 2); err == nil {
		t.Fatal("claimed edit accepted")
	}
}
