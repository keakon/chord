package agent

import (
	"context"
	"github.com/keakon/golog/log"
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
		log.Infof("SubAgent: persisted interrupted tool-call results after interrupt agent=%v turn_id=%v count=%v status=%v", s.instanceID, s.turn.ID, persistedResults, status)
	}
	switch status {
	case ToolResultStatusError:
		emitFailedToolResults(s.parent.emitToTUI, merged, cause)
	default:
		emitCancelledToolResults(s.parent.emitToTUI, merged)
	}
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	log.Infof("SubAgent current turn interrupted agent=%v turn_id=%v pending_tools=%v closed_tools=%v status=%v", s.instanceID, s.turn.ID, pending, len(merged), status)
	return true
}
