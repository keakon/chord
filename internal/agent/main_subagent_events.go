package agent

import (
	"fmt"
	"github.com/keakon/golog/log"
	"strings"
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
	sub.setState(payload.State, payload.Summary)
	a.noteSubAgentStateTransition(sub, payload.State)
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	if strings.TrimSpace(payload.Summary) == "" {
		return
	}
	switch payload.State {
	case SubAgentStateCompleted:
		a.loopState.markProgress()
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "done", Message: payload.Summary})
	case SubAgentStateWaitingPrimary:
		a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: "waiting_primary", Message: payload.Summary})
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
	a.drainOwnedSubAgentMailboxes(evt.SourceID)
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
	sub.setState(SubAgentStateRunning, summary)
	a.noteSubAgentStateTransition(sub, SubAgentStateRunning)
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, "")
	log.Infof("SubAgent progress updated agent=%v summary_len=%v", evt.SourceID, len(summary))
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
	sub.setState(finalState, reason)
	a.noteSubAgentStateTransition(sub, finalState)
	a.persistSubAgentMeta(sub)
	a.syncTaskRecordFromSub(sub, closedReason)
	status := string(finalState)
	switch finalState {
	case SubAgentStateCompleted:
		status = "done"
	case SubAgentStateFailed:
		status = "error"
	}
	a.emitToTUI(AgentStatusEvent{AgentID: evt.SourceID, Status: status, Message: reason})
	a.closeSubAgent(evt.SourceID)
}

func firstReplyMessageID(sub *SubAgent) string {
	if sub == nil {
		return ""
	}
	replyMessageID, _, _, _ := sub.LastReplyThread()
	return replyMessageID
}
