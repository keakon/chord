package agent

import (
	"context"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) interruptSubAgentTurnsForUserCancel() bool {
	// Walk every live runtime, not only *SubAgent: an external coding-agent
	// worker has no Chord turn to interrupt, but a user cancel must still stop
	// its process and settle its task (see requestUserCancel).
	runtimes := a.subs.snapshotRuntimes()
	seen := make(map[string]struct{}, len(runtimes)+1)
	for _, rt := range runtimes {
		if id := rt.InstanceID(); id != "" {
			seen[id] = struct{}{}
		}
	}
	if focused := a.validFocusedSubAgent(); focused != nil {
		if _, ok := seen[focused.instanceID]; !ok {
			runtimes = append(runtimes, focused)
		}
	}

	cancelled := false
	cancelledTaskIDs := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		if worker, ok := rt.(*externalWorker); ok {
			if worker.requestUserCancel("stopped by user") {
				if taskID := strings.TrimSpace(worker.taskID); taskID != "" {
					cancelledTaskIDs = append(cancelledTaskIDs, taskID)
				}
				cancelled = true
			}
			continue
		}
		sub := asSubAgent(rt)
		if sub == nil {
			continue
		}
		interrupted := sub.interruptCurrentTurnWithStatus(message.ToolStatusError, context.Canceled, true)
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
				status = a.terminalStatusAfterCommit(sub, SubAgentStateCancelled, err)
			} else {
				if !sub.setState(SubAgentStateCancelled, "stopped by user") {
					continue
				}
				a.noteSubAgentStateTransition(sub, SubAgentStateCancelled)
				a.persistSubAgentMeta(sub)
				a.syncTaskRecordFromSub(sub, "")
			}
			a.emitToTUI(AgentStatusEvent{AgentID: sub.instanceID, Status: string(status), Message: "Stopped by user"})
			if strings.TrimSpace(sub.taskID) != "" {
				cancelledTaskIDs = append(cancelledTaskIDs, strings.TrimSpace(sub.taskID))
			}
			cancelled = true
		}
	}
	// A parked owner that delegated with child_join is invisible to the walk
	// above (it has no runtime), and a user cancel produces no mailbox, so its
	// wait would never be resolved. Settle those ancestors now.
	if a.settleStrandedWaitingDescendantOwners(cancelledTaskIDs) {
		cancelled = true
	}
	if cancelled {
		a.saveRecoverySnapshot()
	}
	return cancelled
}

func (s *SubAgent) interruptCurrentTurnWithStatus(status message.ToolResultStatus, cause error, _ bool) bool {
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
