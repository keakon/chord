package agent

import (
	"context"
	"log/slog"
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
	turn.PendingToolMeta = nil
	persistedResults := s.persistInterruptedToolResults(merged, ToolResultStatusCancelled, context.Canceled)
	if persistedResults > 0 {
		slog.Info("SubAgent: persisted interrupted tool-call results after cancellation",
			"agent", s.instanceID,
			"turn_id", turn.ID,
			"count", persistedResults,
		)
	}
	emitCancelledToolResults(s.parent.emitToTUI, merged)
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	if s.llmClient != nil {
		s.llmClient.ResetResponsesSession("turn_cancel")
	}
	slog.Info("SubAgent current turn cancelled by user",
		"agent", s.instanceID,
		"turn_id", turn.ID,
		"pending_tools", pending,
		"cancelled_tools", len(merged),
	)
}
