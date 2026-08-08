package agent

import (
	"context"
	"strings"

	"github.com/keakon/golog/log"
)

func (a *MainAgent) interruptSubAgentTurnsForUserCancel() bool {
	a.subs.mu.RLock()
	subs := make([]*SubAgent, 0, len(a.subs.subAgents)+1)
	seen := make(map[string]struct{}, len(a.subs.subAgents)+1)
	for _, sub := range a.subs.subAgents {
		if sub != nil {
			subs = append(subs, sub)
			if sub.instanceID != "" {
				seen[sub.instanceID] = struct{}{}
			}
		}
	}
	a.subs.mu.RUnlock()
	if focused := a.validFocusedSubAgent(); focused != nil {
		if _, ok := seen[focused.instanceID]; !ok {
			subs = append(subs, focused)
		}
	}

	cancelled := false
	for _, sub := range subs {
		interrupted := sub.interruptCurrentTurnWithStatus(ToolResultStatusError, context.Canceled, true)
		state := sub.State()
		if interrupted || state == SubAgentStateRunning || state == SubAgentStateIdle || state == SubAgentStateWaitingMain || state == SubAgentStateWaitingDescendant {
			// A task-backed SubAgent must settle its terminal state: collect
			// and retention only trust settlements, so a bare record flip
			// would leave task.collect(wait) blocked until timeout and the
			// record unarchivable until restore-time migration. If the attempt
			// already settled (the task finished just before the interrupt, or
			// an in-flight request error raced this path), the existing
			// outcome wins and the conflict is only logged.
			status := SubAgentStateCancelled
			if strings.TrimSpace(sub.taskID) != "" {
				_, _, err := a.commitTerminalTask(sub, SubAgentStateCancelled, "stopped by user", "stopped by user", nil)
				if err != nil {
					log.Warnf("user-cancel settlement failed agent=%v task_id=%v error=%v", sub.instanceID, sub.taskID, err)
				}
				status = a.terminalStatusAfterCommit(sub.taskID, SubAgentStateCancelled, err)
			} else {
				sub.setState(SubAgentStateCancelled, "stopped by user")
				a.noteSubAgentStateTransition(sub, SubAgentStateCancelled)
				a.persistSubAgentMeta(sub)
				a.syncTaskRecordFromSub(sub, "")
			}
			a.emitToTUI(AgentStatusEvent{AgentID: sub.instanceID, Status: string(status), Message: "Stopped by user"})
			cancelled = true
		}
	}
	if cancelled {
		a.saveRecoverySnapshot()
	}
	return cancelled
}

func (s *SubAgent) interruptCurrentTurnWithStatus(status ToolResultStatus, cause error, _ bool) bool {
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
	merged = s.turn.filterCompletedToolCalls(merged)

	persistedResults := finalizeInterruptedToolCalls(s.ctxMgr, s.parent.emitToTUI, s.persistInterruptedToolResults, merged, status, cause)
	if persistedResults > 0 {
		log.Infof("SubAgent: persisted interrupted tool-call results after interrupt agent=%v turn_id=%v count=%v status=%v", s.instanceID, s.turn.ID, persistedResults, status)
	}
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	log.Infof("SubAgent current turn interrupted agent=%v turn_id=%v pending_tools=%v closed_tools=%v status=%v", s.instanceID, s.turn.ID, pending, len(merged), status)
	return true
}
