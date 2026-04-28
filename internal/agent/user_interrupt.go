package agent

import (
	"context"
	"log/slog"
)

func (a *MainAgent) interruptSubAgentTurnsForUserCancel() bool {
	a.mu.RLock()
	subs := make([]*SubAgent, 0, len(a.subAgents)+1)
	seen := make(map[string]struct{}, len(a.subAgents)+1)
	for _, sub := range a.subAgents {
		if sub != nil {
			subs = append(subs, sub)
			if sub.instanceID != "" {
				seen[sub.instanceID] = struct{}{}
			}
		}
	}
	a.mu.RUnlock()
	if focused := a.validFocusedSubAgent(); focused != nil {
		if _, ok := seen[focused.instanceID]; !ok {
			subs = append(subs, focused)
		}
	}

	cancelled := false
	for _, sub := range subs {
		if sub.interruptCurrentTurnWithStatus(ToolResultStatusError, context.Canceled, true) {
			cancelled = true
		}
	}
	return cancelled
}

func (s *SubAgent) interruptCurrentTurnWithStatus(status ToolResultStatus, cause error, resetResponses bool) bool {
	if s == nil || s.turn == nil {
		return false
	}

	pending := s.turn.PendingToolCalls.Load()
	if s.turn.activeToolBatchCancel != nil {
		s.turn.activeToolBatchCancel()
		s.turn.activeToolBatchCancel = nil
	}
	s.turn.PendingToolCalls.Store(0)
	s.turn.TotalToolCalls.Store(0)
	s.turn.toolExecutionBatches = nil
	s.turn.nextToolBatch = 0
	s.turn.Cancel()
	cancelledExec := s.turn.cancelPendingToolCalls()
	cancelledStream := s.turn.drainStreamingToolCalls()
	merged := mergePendingToolCalls(cancelledExec, cancelledStream)

	persistedResults := s.persistInterruptedToolResults(merged, status, cause)
	if persistedResults > 0 {
		slog.Info("SubAgent: persisted interrupted tool-call results after interrupt",
			"agent", s.instanceID,
			"turn_id", s.turn.ID,
			"count", persistedResults,
			"status", status,
		)
	}
	switch status {
	case ToolResultStatusError:
		emitFailedToolResults(s.parent.emitToTUI, merged, cause)
	default:
		emitCancelledToolResults(s.parent.emitToTUI, merged)
	}
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	if resetResponses && s.llmClient != nil {
		s.llmClient.ResetResponsesSession("turn_cancel")
	}
	slog.Info("SubAgent current turn interrupted",
		"agent", s.instanceID,
		"turn_id", s.turn.ID,
		"pending_tools", pending,
		"closed_tools", len(merged),
		"status", status,
	)
	return true
}
