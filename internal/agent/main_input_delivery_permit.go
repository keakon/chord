package agent

import (
	"slices"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/recovery"
)

// A failed permit retains its exact continuation. Reconciliation alone cannot
// authorize execution; retry the transcript barrier and durable permit first.
func (a *MainAgent) retryDeliveryPermit() bool {
	permit := a.delivery.permit
	if permit == nil {
		return false
	}
	if !permit.resumeAllowed() {
		a.delivery.permit = nil
		a.delivery.request = nil
		return true
	}
	a.delivery.pending = append(a.delivery.pending, permit)
	a.startNextDeliveryOperation()
	return true
}

func (a *MainAgent) newDeliveryPermit(ids []string, workID string, dir WorkDirSnapshot, turnID, turnEpoch, cancelGeneration uint64) *deliveryOperation {
	permit := &deliveryOperation{syncTranscript: true}
	permit.resumeAllowed = func() bool {
		currentDir := a.WorkDirSnapshot()
		return !a.sessionExecutionPaused() && !a.delivery.navigation && !a.shuttingDown.Load() && a.turn != nil && a.turn.ID == turnID && a.turn.Epoch == turnEpoch && a.turn.Ctx.Err() == nil && a.delivery.cancelGeneration.Load() == cancelGeneration && currentDir.Path == dir.Path && currentDir.WorktreeID == dir.WorktreeID
	}
	permit.mutate = func(state *recovery.InputDeliveryState) error {
		if !permit.resumeAllowed() {
			return recovery.ErrInputDeliveryConflict
		}
		// A failed permit callback never dispatched the request, including when
		// rename succeeded but its directory sync failed. This live continuation
		// can reacquire the permit; restored processes have no such continuation.
		for i := range state.Works {
			work := &state.Works[i]
			if work.WorkID == workID && work.Outcome == recovery.DeliveryWorkUnknown {
				work.Outcome = recovery.DeliveryWorkActive
			}
		}
		matched := 0
		for i := range state.Inputs {
			input := &state.Inputs[i]
			if slices.Contains(ids, input.InputID) {
				matched++
				if (input.Stage != recovery.PendingInputStageClaimed && input.Stage != recovery.PendingInputStageStarted) || input.WorkID != workID {
					return recovery.ErrInputDeliveryConflict
				}
				input.Stage = recovery.PendingInputStageStarted
				input.Revision++
			}
		}
		if matched != len(ids) {
			return recovery.ErrInputDeliveryConflict
		}
		return nil
	}
	permit.finish = func() {
		a.delivery.permit = nil
		if a.sessionExecutionPaused() || a.delivery.navigation {
			return
		}
		if a.turn == nil || a.turn.ID != turnID || a.turn.Epoch != turnEpoch || a.turn.Ctx.Err() != nil || a.delivery.cancelGeneration.Load() != cancelGeneration {
			a.settleDeliveryWork(workID, recovery.DeliveryWorkCancelled)
			return
		}
		currentDir := a.WorkDirSnapshot()
		if currentDir.Path != dir.Path || currentDir.WorktreeID != dir.WorktreeID {
			a.settleDeliveryWork(workID, recovery.DeliveryWorkUnknown)
			return
		}
		for _, input := range a.delivery.committed.Inputs {
			if slices.Contains(ids, input.InputID) {
				a.emitPendingDraftConsumed(input.DraftID, message.Message{Role: message.RoleUser, InputID: input.InputID, InputRequestID: input.RequestID, Content: input.Content, Parts: input.Parts})
			}
		}
		if a.delivery.request == nil {
			a.delivery.request = &deliveryRequest{turnID: turnID, turnEpoch: turnEpoch}
		}
	}
	return permit
}
