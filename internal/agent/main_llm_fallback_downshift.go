package agent

import (
	"fmt"

	"github.com/keakon/golog/log"
)

func (a *MainAgent) handleCompactionDownshiftSuspend(evt Event) {
	payload, ok := evt.Payload.(*pendingMainLLMCall)
	if !ok || payload == nil {
		log.Errorf("handleCompactionDownshiftSuspend: invalid payload type=%v", fmt.Sprintf("%T", evt.Payload))
		return
	}
	if a.turn == nil || payload.turnID == 0 || payload.turnID != a.turn.ID ||
		payload.turnEpoch != a.turn.Epoch || payload.sessionEpoch != a.sessionEpoch {
		log.Debugf("discarding stale downshift suspension turn_id=%v plan_id=%v", payload.turnID, payload.planID)
		return
	}
	if !a.IsCompactionRunning() || a.compactionState.planID != payload.planID {
		log.Debugf("downshift suspension arrived after compaction settled turn_id=%v plan_id=%v", payload.turnID, payload.planID)
		return
	}
	a.compactionState.continuation = continuationPlan{
		kind:             compactionResumeMainLLM,
		turnID:           payload.turnID,
		turnEpoch:        payload.turnEpoch,
		agentErrSourceID: payload.agentErrSourceID,
	}
	a.compactionState.downshiftSuspended = true
	// The provider response has reached the event loop and is now suspended;
	// only now may the background compaction reclaim the shared activity slot.
	a.handoffMainActivityToCompaction()
}
