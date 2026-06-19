package agent

import (
	"context"

	"github.com/keakon/golog/log"
)

func (s *SubAgent) cancelCurrentTurnFromLoop() {
	if s == nil {
		return
	}
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	if s.turn == nil {
		return
	}
	turn := s.turn
	pending := turn.PendingToolCalls.Load()
	if turn.activeToolBatchCancel != nil {
		turn.activeToolBatchCancel()
		turn.activeToolBatchCancel = nil
	}
	turn.PendingToolCalls.Store(0)
	turn.TotalToolCalls.Store(0)
	turn.toolExecutionBatches = nil
	turn.nextToolBatch = 0
	turn.Cancel()
	merged := turn.snapshotPendingToolCalls()
	merged = turn.filterCompletedToolCalls(merged)
	turn.PendingToolMeta = nil
	persistedResults := finalizeInterruptedToolCalls(s.ctxMgr, s.parent.emitToTUI, s.persistInterruptedToolResults, merged, ToolResultStatusCancelled, context.Canceled)
	if persistedResults > 0 {
		log.Infof("SubAgent: persisted interrupted tool-call results after cancellation agent=%v turn_id=%v count=%v", s.instanceID, turn.ID, persistedResults)
	}
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	log.Infof("SubAgent current turn cancelled by user agent=%v turn_id=%v pending_tools=%v cancelled_tools=%v", s.instanceID, turn.ID, pending, len(merged))
}
