package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/tools"
)

func (a *MainAgent) handleSubAgentStateChangedEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentStateChangedPayload)
	if !ok || payload == nil {
		return
	}
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		return
	}
	if payload.InstanceID != "" && payload.InstanceID != sub.instanceID {
		log.Debugf("dropping stale subagent state event instance=%v current=%v", payload.InstanceID, sub.instanceID)
		return
	}
	if payload.TaskID != "" && payload.TaskID != sub.taskID {
		log.Debugf("dropping stale subagent state event task=%v current=%v", payload.TaskID, sub.taskID)
		return
	}
	if payload.Attempt != 0 {
		record := a.taskRecordByTaskID(sub.taskID)
		if record == nil || record.Attempt != payload.Attempt {
			log.Debugf("dropping stale subagent state event task=%v attempt=%v", sub.taskID, payload.Attempt)
			return
		}
	}
	if !sub.setState(payload.State, payload.Summary) {
		return
	}
	a.noteSubAgentStateTransition(sub, payload.State)
	switch payload.State {
	// Terminal states persist synchronously: the final state must be durable
	// before the event loop moves on. Everything else coalesces through the
	// debounced flush so bursts of worker events do not stall dispatch.
	case SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
		a.persistSubAgentMeta(sub)
		a.syncTaskRecordFromSub(sub, "")
	default:
		a.syncSubAgentPersists(sub, "")
	}
	if strings.TrimSpace(payload.Summary) == "" {
		return
	}
	switch payload.State {
	case SubAgentStateCompleted:
		a.loopState.markProgress()
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "done", Message: payload.Summary})
	case SubAgentStateWaitingMain:
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "waiting_main", Message: payload.Summary})
	case SubAgentStateWaitingDescendant:
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: string(SubAgentStateWaitingDescendant), Message: payload.Summary})
	case SubAgentStateRunning:
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "running", Message: payload.Summary})
	case SubAgentStateFailed:
		a.loopState.markProgress()
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "error", Message: payload.Summary})
	case SubAgentStateCancelled:
		a.loopState.markProgress()
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: string(SubAgentStateCancelled), Message: payload.Summary})
	}
}

func (a *MainAgent) handleSubAgentProgressUpdatedEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentProgressUpdatedPayload)
	if !ok || payload == nil {
		return
	}
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		log.Debugf("dropping subagent progress update from abandoned agent agent_id=%v", evt.SourceID)
		return
	}
	summary := strings.TrimSpace(payload.Summary)
	if summary == "" {
		return
	}
	if !sub.updateProgress(summary) {
		return
	}
	a.syncSubAgentPersists(sub, "")
	log.Debugf("SubAgent progress updated agent=%v summary_len=%v", evt.SourceID, len(summary))
}

func (a *MainAgent) handleSubAgentCloseRequestedEvent(evt Event) {
	payload, ok := evt.Payload.(*SubAgentCloseRequestedPayload)
	if !ok || payload == nil {
		return
	}
	sub := a.subAgentByID(evt.SourceID)
	if sub == nil {
		return
	}
	finalState := payload.FinalState
	if finalState == "" {
		finalState = SubAgentStateCancelled
	}
	reason := strings.TrimSpace(payload.Reason)
	if reason == "" {
		reason = fmt.Sprintf("SubAgent %s closed", evt.SourceID)
	}
	closedReason := strings.TrimSpace(payload.ClosedReason)
	if closedReason == "" {
		closedReason = reason
	}
	statusState := finalState
	if isTerminalSubAgentState(finalState) {
		_, _, err := a.commitTerminalTask(sub, finalState, reason, closedReason, payload.Completion)
		if err != nil {
			log.Warnf("terminal settlement failed agent=%v task_id=%v error=%v", sub.instanceID, sub.taskID, err)
			statusState = a.terminalStatusAfterCommit(sub, finalState, err)
		}
	} else {
		if !sub.setState(finalState, reason) {
			return
		}
		a.noteSubAgentStateTransition(sub, finalState)
		a.persistSubAgentMeta(sub)
		a.syncTaskRecordFromSub(sub, closedReason)
	}
	a.reconcileTerminalTaskChildren(sub.taskID, statusState, closedReason)
	status := string(statusState)
	switch statusState {
	case SubAgentStateCompleted:
		status = "done"
	case SubAgentStateFailed:
		status = "error"
	}
	a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: status, Message: reason})
	a.releaseSubAgentSlot(sub)
	a.fileTrack.ReleaseAll(evt.SourceID)
	tools.StopAllJobsForAgent(evt.SourceID, "terminated on subagent stop")
	a.parkSubAgent(evt.SourceID)
}

func firstReplyMessageID(sub *SubAgent) string {
	if sub == nil {
		return ""
	}
	replyMessageID, _, _, _ := sub.LastReplyThread()
	return replyMessageID
}
