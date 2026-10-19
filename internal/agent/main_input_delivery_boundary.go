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
			if outcome != recovery.DeliveryWorkCompleted && state.Gate.Mode == recovery.FollowupGateRunning {
				reason := recovery.FollowupPredecessorFailed
				if outcome == recovery.DeliveryWorkCancelled {
					reason = recovery.FollowupPredecessorCancelled
				}
				if outcome == recovery.DeliveryWorkUnknown {
					reason = recovery.FollowupOutcomeUnknown
				}
				state.Gate.Mode, state.Gate.Reason = recovery.FollowupGatePaused, reason
			}
		}
		// A failure only pauses the queue to protect follow-ups queued behind
		// it; with none waiting the pause would outlive its cause and block
		// later follow-ups until an unexplained /queue resume.
		normalizeFollowupGate(state)
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
	referenced := map[string]bool{state.Gate.WaitingWorkID: true, currentWorkID: true}
	for _, input := range state.Inputs {
		referenced[input.WorkID] = true
	}
	state.Works = slices.DeleteFunc(state.Works, func(work recovery.DeliveryWork) bool { return !referenced[work.WorkID] })
}

func (a *MainAgent) pauseInputDeliveryForCancel() {
	if a.delivery.committed == nil && a.delivery.inflight == nil && len(a.delivery.pending) == 0 {
		return
	}
	op := &deliveryOperation{mutate: func(state *recovery.InputDeliveryState) error {
		if state.Gate.Mode == recovery.FollowupGateRunning {
			state.Gate.Mode, state.Gate.Reason = recovery.FollowupGatePaused, recovery.FollowupPredecessorCancelled
		}
		return nil
	}}
	a.delivery.pending = append(a.delivery.pending, op)
	a.startNextDeliveryOperation()
}
