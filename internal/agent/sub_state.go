package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func (s *SubAgent) State() SubAgentState {
	state, _ := s.runtimeState.snapshot()
	if state == "" {
		return SubAgentStateRunning
	}
	return state
}

func (s *SubAgent) LastSummary() string {
	_, summary := s.runtimeState.snapshot()
	return summary
}

func (s *SubAgent) LastMailboxID() string {
	lastMailboxID, _, _, _, _ := s.runtimeState.mailboxThreadSnapshot()
	return lastMailboxID
}

func (s *SubAgent) LastReplyThread() (replyMessageID, replyToMailboxID, replyKind, replySummary string) {
	_, replyMessageID, replyToMailboxID, replyKind, replySummary = s.runtimeState.mailboxThreadSnapshot()
	return replyMessageID, replyToMailboxID, replyKind, replySummary
}

func (s *SubAgent) LastArtifact() tools.ArtifactRef {
	ref, _ := s.runtimeState.artifactSnapshot()
	return ref
}

func (s *SubAgent) OwnerAgentID() string {
	return strings.TrimSpace(s.ownerAgentID)
}

func (s *SubAgent) OwnerTaskID() string {
	return strings.TrimSpace(s.ownerTaskID)
}

func (s *SubAgent) Depth() int {
	return s.depth
}

func (s *SubAgent) PendingCompleteIntent() *AgentResult {
	return s.runtimeState.pendingCompleteSnapshot()
}

func (s *SubAgent) setPendingCompleteIntent(result *AgentResult) {
	s.runtimeState.setPendingComplete(result)
}

func (s *SubAgent) clearPendingCompleteIntent() {
	s.runtimeState.clearPendingComplete()
}

func deferredCompleteResult(count int) string {
	switch {
	case count <= 0:
		return "Completion deferred: waiting for child task before final completion."
	case count == 1:
		return "Completion deferred: waiting for 1 child task before final completion."
	default:
		return fmt.Sprintf("Completion deferred: waiting for %d child tasks before final completion.", count)
	}
}

func (s *SubAgent) enterWaitingDescendant(reason string) {
	if s == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "Waiting for child task"
	}
	s.setState(SubAgentStateWaitingDescendant, reason)
	s.parent.noteSubAgentStateTransition(s, SubAgentStateWaitingDescendant)
	if s.semHeld {
		s.parent.releaseSubAgentSlot(s)
	}
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	s.parent.emitToTUI(AgentStatusEvent{
		AgentID: s.instanceID,
		Status:  string(SubAgentStateWaitingDescendant),
		Message: reason,
	})
	s.parent.persistSubAgentMeta(s)
	s.parent.syncTaskRecordFromSub(s, "")
	s.parent.saveRecoverySnapshot()
}

func (s *SubAgent) appendCompleteToolResult(callID, resultContent string) {
	if strings.TrimSpace(callID) == "" {
		return
	}
	toolMsg := message.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    resultContent,
	}
	s.ctxMgr.Append(toolMsg)
	if s.recovery != nil {
		go func(msg message.Message) {
			if err := s.recovery.PersistMessage(s.instanceID, msg); err != nil {
				log.Warnf("SubAgent: failed to persist Complete tool result agent=%v error=%v", s.instanceID, err)
			}
		}(toolMsg)
	}
	s.turn.removeStreamingToolCall(callID)
	s.parent.emitToTUI(ToolResultEvent{
		CallID:  callID,
		Result:  resultContent,
		Status:  ToolResultStatusSuccess,
		AgentID: s.instanceID,
	})
}

func (s *SubAgent) StateChangedAt() time.Time {
	_, changedAt := s.runtimeState.artifactSnapshot()
	return changedAt
}

func (s *SubAgent) setState(state SubAgentState, summary string) {
	s.runtimeState.set(state, summary)
}

func (s *SubAgent) setLastMailboxID(id string) {
	s.runtimeState.setLastMailboxID(id)
}

func (s *SubAgent) setReplyThread(replyMessageID, replyToMailboxID, replyKind, replySummary string) {
	s.runtimeState.setReplyThread(replyMessageID, replyToMailboxID, replyKind, replySummary)
}

func (s *SubAgent) setLastArtifact(ref tools.ArtifactRef) {
	s.runtimeState.setLastArtifact(ref)
}
