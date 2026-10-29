package agent

import (
	"crypto/rand"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/recovery"
)

func (a *MainAgent) newDeliveryWork(origin string) string {
	a.delivery.currentWorkID = rand.Text()
	a.delivery.currentOrigin = origin
	return a.delivery.currentWorkID
}

func isDeliveryFinalResponse(payload *LLMResponsePayload) bool {
	if payload == nil || strings.TrimSpace(payload.Content) == "" || len(payload.ToolCalls) > 0 || (payload.NativeTools != nil && payload.NativeTools.OutcomeUnknown) {
		return false
	}
	switch payload.StopReason {
	case "stop", "end_turn", "stop_sequence", "STOP":
		return true
	}
	return false
}

func (a *MainAgent) settleDeliveryWork(workID, outcome string) {
	if workID == "" {
		return
	}
	tracked := false
	if state := a.delivery.committed; state != nil {
		for _, work := range state.Works {
			if work.WorkID == workID {
				tracked = true
				break
			}
		}
	}
	if !tracked && len(a.delivery.pending) == 0 && a.delivery.inflight == nil {
		if a.delivery.currentWorkID == workID {
			a.delivery.currentWorkID = ""
		}
		return
	}
	op := &deliveryOperation{syncTranscript: outcome == recovery.DeliveryWorkCompleted}
	op.mutate = func(state *recovery.InputDeliveryState) error {
		for i := range state.Works {
			work := &state.Works[i]
			if work.WorkID != workID {
				continue
			}
			if work.Outcome != recovery.DeliveryWorkActive {
				return nil
			}
			work.Outcome = outcome
			present := make(map[string]bool)
			for _, msg := range a.ctxMgr.Snapshot() {
				if msg.InputID != "" {
					present[msg.InputID] = true
				}
			}
			// A claim is a reservation, not evidence that the input was consumed.
			for j := range state.Inputs {
				input := &state.Inputs[j]
				if input.WorkID == workID && input.Stage == recovery.PendingInputStageClaimed && !present[input.InputID] {
					input.Stage, input.WorkID = recovery.PendingInputStageQueued, ""
					input.Revision++
				}
			}
			state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool { return input.WorkID == workID })
		}
		return nil
	}
	op.finish = func() {
		if a.delivery.currentWorkID == workID {
			a.delivery.currentWorkID = ""
		}
		a.queueLoopEvent(Event{Type: eventInputDeliveryAdvance})
	}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}

func pruneDeliveryWorks(state *recovery.InputDeliveryState, currentWorkID string) {
	referenced := map[string]bool{currentWorkID: true}
	for _, input := range state.Inputs {
		referenced[input.WorkID] = true
		referenced[input.WaitWorkID] = true
	}
	state.Works = slices.DeleteFunc(state.Works, func(work recovery.DeliveryWork) bool { return !referenced[work.WorkID] })
}

// rolloverDeliveryWaits re-points every queued after-completion input at the
// work this new turn runs. "After completion" means after whatever most
// recently started, so follow-ups chain one behind another: when one of them
// runs, the rest wait for it, and its failure, cancellation or crash holds
// them until the user's next request — itself a new turn — re-bases the wait.
// The same op retires rows left by an interrupted work whose outcome is
// unknown: they exist so an explicit continue can adopt them, and a new turn
// that did not adopt them supersedes that work.
//
// The op is queued only when there is something to change, keeping ordinary
// turns free of queue writes.
func (a *MainAgent) rolloverDeliveryWaits(workID string) {
	if workID == "" || !a.deliveryNeedsRollover(workID) {
		return
	}
	origin := a.delivery.currentOrigin
	op := &deliveryOperation{}
	op.mutate = func(state *recovery.InputDeliveryState) error {
		for i := range state.Inputs {
			if input := &state.Inputs[i]; queuedAfterCompletion(*input) {
				input.WaitWorkID = workID
			}
		}
		state.Inputs = slices.DeleteFunc(state.Inputs, func(input recovery.PendingInput) bool {
			return orphanedByUnknownWork(state, input, workID)
		})
		ensureDeliveryWork(state, workID, origin)
		return nil
	}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}

func queuedAfterCompletion(input recovery.PendingInput) bool {
	return input.Stage == recovery.PendingInputStageQueued && input.DeliveryPolicy == recovery.InputDeliveryAfterCompletion
}

func orphanedByUnknownWork(state *recovery.InputDeliveryState, input recovery.PendingInput, liveWorkID string) bool {
	if input.Stage == recovery.PendingInputStageQueued || input.WorkID == "" || input.WorkID == liveWorkID {
		return false
	}
	for _, work := range state.Works {
		if work.WorkID == input.WorkID {
			return work.Outcome == recovery.DeliveryWorkUnknown
		}
	}
	return false
}

func (a *MainAgent) deliveryNeedsRollover(liveWorkID string) bool {
	state := a.delivery.committed
	if state == nil {
		return false
	}
	for _, input := range state.Inputs {
		if queuedAfterCompletion(input) && input.WaitWorkID != liveWorkID || orphanedByUnknownWork(state, input, liveWorkID) {
			return true
		}
	}
	return false
}
