package recovery

import (
	"slices"

	"github.com/keakon/chord/internal/message"
)

// ReconcileInputDelivery never grants permission to execute. In particular,
// transcript presence proves append, not whether a request reached a provider.
// The returned candidate must be saved before accepting further operations.
//
// A queued row whose input already reached the transcript is retired: the row
// was never dispatched, so the transcript is the authoritative copy of the
// request. This is the recovery side of writing the shutdown commit to the
// transcript before retiring its queue row.
//
// Claimed and started rows whose input already reached the transcript are kept.
// Retiring a claimed row would erase the evidence prepareInputDeliveryRequest
// uses to refuse a live turn that lost its execution permit — turning "permit
// unknown" into "nothing to deliver" — and a started row is the durable record
// of an interrupted work whose outcome is unknown. Settlement retires rows on
// its own path once the continued work settles.
//
// liveWorkID names the work of a turn still running in this process (a reload
// after storage recovery happens mid-turn); it stays active. Every other work
// recorded as active belongs to a turn that died.
func ReconcileInputDelivery(state *InputDeliveryState, transcript []message.Message, liveWorkID string) *InputDeliveryState {
	out := state.Clone()
	if out == nil {
		return nil
	}
	present := make(map[string]bool)
	for _, msg := range transcript {
		if msg.InputID != "" {
			present[msg.InputID] = true
		}
	}
	for i := range out.Inputs {
		input := &out.Inputs[i]
		switch input.Stage {
		case PendingInputStageClaimed:
			if !present[input.InputID] {
				input.Stage, input.WorkID = PendingInputStageQueued, ""
				input.Revision++
			}
		case PendingInputStageStarted:
			for j := range out.Works {
				work := &out.Works[j]
				if work.WorkID == input.WorkID && work.WorkID != liveWorkID && work.Outcome == DeliveryWorkActive {
					work.Outcome = DeliveryWorkUnknown
				}
			}
		}
	}
	// A work still recorded as active that is not the live turn's belongs to a
	// turn that died: it will never settle, and waits pinned to it must read as
	// unresolved, not as forever-pending.
	for j := range out.Works {
		if out.Works[j].Outcome == DeliveryWorkActive && out.Works[j].WorkID != liveWorkID {
			out.Works[j].Outcome = DeliveryWorkUnknown
		}
	}
	out.Inputs = slices.DeleteFunc(out.Inputs, func(input PendingInput) bool {
		return input.Stage == PendingInputStageQueued && present[input.InputID]
	})
	referenced := make(map[string]bool)
	for _, input := range out.Inputs {
		if input.WorkID != "" {
			referenced[input.WorkID] = true
		}
		if input.WaitWorkID != "" {
			referenced[input.WaitWorkID] = true
		}
	}
	out.Works = slices.DeleteFunc(out.Works, func(work DeliveryWork) bool { return !referenced[work.WorkID] })
	out.Revision++
	return out
}
