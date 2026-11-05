package agent

import (
	"context"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
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

	// Parent control paths also call this under turnMu. Directory discovery
	// stays owner-only; runLoop observes cancellation and clears it.
	turn := s.turn
	if turn.deliveryPending.Load() {
		turn.deliverySubCancel.Store(true)
		turn.Cancel()
		return
	}
	pending := turn.PendingToolCalls.Load()
	if turn.activeToolBatchCancel != nil {
		turn.activeToolBatchCancel()
		turn.activeToolBatchCancel = nil
	}
	// Close admissions and collect every result that already finished before
	// the turn is cancelled: a completed result outranks the cancellation,
	// while an admitted call without a result must report outcome_unknown.
	evidence := s.collectToolSettlement(turn)
	turn.PendingToolCalls.Store(0)
	turn.TotalToolCalls.Store(0)
	turn.toolExecutionBatches = nil
	turn.nextToolBatch = 0
	turn.Cancel()
	merged := turn.snapshotPendingToolCalls()
	merged = turn.filterCompletedToolCalls(merged)
	synthetic, preserved := s.settleInterruptedSubToolCalls(turn, evidence, merged)
	turn.PendingToolMeta = nil
	persistedResults := finalizeInterruptedToolCalls(s.ctxMgr, s.parent.emitToTUI, s.persistInterruptedToolResults, synthetic, message.ToolStatusCancelled, context.Canceled)
	if persistedResults > 0 {
		log.Infof("SubAgent: persisted interrupted tool-call results after cancellation agent=%v turn_id=%v count=%v", s.instanceID, turn.ID, persistedResults)
	}
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	log.Infof("SubAgent current turn cancelled by user agent=%v turn_id=%v pending_tools=%v cancelled_tools=%v completed_tools=%v", s.instanceID, turn.ID, pending, len(synthetic), preserved)
}
