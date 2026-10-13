package agent

import (
	"fmt"

	"github.com/keakon/chord/internal/recovery"
)

func (a *MainAgent) initializeInputDelivery() {
	owner := a.currentRecoveryOwnership()
	if owner == nil || a.delivery.owner == owner || a.delivery.inflight != nil {
		return
	}
	if previous := a.delivery.owner; previous != nil && previous.epoch == owner.epoch && previous.dir == owner.dir {
		a.delivery.owner = owner
		return
	}
	a.delivery.pending = append(a.delivery.pending, &deliveryOperation{loadOnly: true})
	a.startNextDeliveryOperation()
}

func (a *MainAgent) deferDeliverySessionSwitch(resume func()) bool {
	if a.delivery.navigationReady {
		a.delivery.navigationReady = false
		return false
	}
	if a.delivery.navigation {
		return true
	}
	if a.delivery.committed == nil && a.delivery.inflight == nil && len(a.delivery.pending) == 0 {
		return false
	}
	if a.delivery.persistenceError != nil {
		a.emitToTUI(ToastEvent{Message: "Could not save the input queue; resolve its storage error before switching sessions", Level: "warn"})
		return true
	}
	a.delivery.navigation = true
	a.delivery.cancelGeneration.Add(1)
	op := &deliveryOperation{}
	op.mutate = func(state *recovery.InputDeliveryState) error {
		if state.Gate.Mode == recovery.FollowupGateRunning {
			state.Gate.Mode, state.Gate.Reason = recovery.FollowupGatePaused, recovery.FollowupSessionSwitched
		}
		return nil
	}
	op.failed = func(err error) {
		a.delivery.navigation = false
		a.emitToTUI(ToastEvent{Message: err.Error(), Level: "warn"})
	}
	op.finish = func() { a.delivery.navigation = false; a.delivery.navigationReady = true; resume() }
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
	return true
}

func (a *MainAgent) resetInputDeliverySession() {
	if a.delivery.inflight != nil || len(a.delivery.pending) > 0 {
		a.delivery.persistenceError = fmt.Errorf("session changed with unresolved input queue operations")
		return
	}
	a.delivery.committed = nil
	a.delivery.loading = false
	a.delivery.environmentError = ""
	a.delivery.owner = nil
	a.delivery.request = nil
	a.delivery.permit = nil
	a.delivery.currentWorkID = ""
	a.delivery.currentOrigin = ""
	a.delivery.acceptedOrders = nil
	a.delivery.persistenceError = nil
	a.delivery.restoredNextInputsBlocked = false
	a.delivery.snapshot.Store(nil)
}

// Explicit context continuation adopts already-consumed inputs without appending
// their bodies again. The new permit follows a transcript barrier as usual.
func (a *MainAgent) adoptConsumedDeliveryInputs(workID string) {
	state := a.delivery.committed
	if state == nil {
		return
	}
	found := false
	for _, input := range state.Inputs {
		if input.Stage != recovery.PendingInputStageQueued {
			found = true
		}
	}
	if !found {
		return
	}
	op := &deliveryOperation{syncTranscript: true, mutate: func(state *recovery.InputDeliveryState) error {
		ensureDeliveryWork(state, workID, recovery.DeliveryWorkContext)
		for i := range state.Inputs {
			input := &state.Inputs[i]
			if input.Stage != recovery.PendingInputStageQueued {
				input.WorkID = workID
				input.Stage = recovery.PendingInputStageStarted
				input.Revision++
			}
		}
		return nil
	}}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}

func (a *MainAgent) finishDeliveryResumeOperations(err error) {
	ops := a.delivery.resumeOperations
	a.delivery.resumeOperations = nil
	for _, op := range ops {
		if err != nil {
			a.rejectDeliveryOperation(op, InputQueueRejected, err)
		} else {
			a.resumePendingUserDrain()
			a.handleDeliveryOperation(op)
		}
	}
}
