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
	if a.compactionState.continuation.kind == compactionResumeModelDriven {
		// A model-driven checkpoint already owns this turn's continuation: its
		// settle path is what emits the compact_context tool result. Replacing
		// it with a plain main_llm resume would leave that tool call
		// unanswered and break the next request's message structure. The
		// barrier freezes the turn's request, so no in-flight call should
		// reach the fallback boundary while one is running.
		log.Warnf("downshift suspension arrived while a model-driven checkpoint owns the continuation; keeping it turn_id=%v plan_id=%v", payload.turnID, payload.planID)
	} else {
		a.compactionState.continuation = continuationPlan{
			kind:             compactionResumeMainLLM,
			turnID:           payload.turnID,
			turnEpoch:        payload.turnEpoch,
			agentErrSourceID: payload.agentErrSourceID,
		}
	}
	a.compactionState.downshiftSuspended = true
	// The provider response has reached the event loop and is now suspended;
	// only now may the background compaction reclaim the shared activity slot.
	a.handoffMainActivityToCompaction()
	a.applyDraftParkedAtBarrier()
}
