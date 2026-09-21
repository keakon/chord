package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// currentRuleset returns the SubAgent's published permission snapshot. Callers
// must treat the result as immutable; see the rulesetPtr field comment.
func (s *SubAgent) currentRuleset() permission.Ruleset {
	if s == nil {
		return nil
	}
	if published := s.rulesetPtr.Load(); published != nil {
		return *published
	}
	return nil
}

// setRuleset publishes a freshly built ruleset snapshot for this SubAgent.
func (s *SubAgent) setRuleset(ruleset permission.Ruleset) {
	if s == nil {
		return
	}
	s.rulesetPtr.Store(&ruleset)
}

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
	ownerAgentID, _, _, _ := s.ownerSnapshot()
	return ownerAgentID
}

func (s *SubAgent) OwnerTaskID() string {
	_, ownerTaskID, _, _ := s.ownerSnapshot()
	return ownerTaskID
}

func (s *SubAgent) ownerSnapshot() (ownerAgentID, ownerTaskID string, depth int, joinToOwner bool) {
	if s == nil {
		return "", "", 0, false
	}
	s.ownerMu.RLock()
	defer s.ownerMu.RUnlock()
	return strings.TrimSpace(s.ownerAgentID), strings.TrimSpace(s.ownerTaskID), s.depth, s.joinToOwner
}

func (s *SubAgent) reparentToMain() {
	if s == nil {
		return
	}
	s.ownerMu.Lock()
	s.ownerAgentID = ""
	s.ownerTaskID = ""
	s.depth = 1
	s.joinToOwner = false
	s.ownerMu.Unlock()
}

func (s *SubAgent) Depth() int {
	_, _, depth, _ := s.ownerSnapshot()
	return depth
}

func (s *SubAgent) JoinToOwner() bool {
	_, _, _, joinToOwner := s.ownerSnapshot()
	return joinToOwner
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
	if !s.setState(SubAgentStateWaitingDescendant, reason) {
		log.Warnf("sub-agent cannot enter waiting_descendant agent=%v", s.instanceID)
		return
	}
	s.parent.noteSubAgentStateTransition(s, SubAgentStateWaitingDescendant)
	s.parent.releaseSubAgentSlot(s)
	s.parent.emitActivity(s.instanceID, ActivityIdle, "")
	s.parent.emitToTUI(AgentStatusEvent{
		AgentID: s.instanceID,
		Status:  string(SubAgentStateWaitingDescendant),
		Message: reason,
	})
	s.parent.syncSubAgentPersists(s, "")
	s.parent.saveRecoverySnapshot()
	s.parent.parkSubAgent(s.instanceID)
}

// appendCompleteToolResult records the engine's answer to a Complete call.
// The status is explicit per call site: a deferred delivery was accepted, a
// rejected one was not, and the card (live and after restore) must agree with
// the transcript instead of claiming success for both.
func (s *SubAgent) appendCompleteToolResult(callID, resultContent string, status ToolResultStatus) {
	s.appendControlToolResult(callID, tools.NameComplete, "", resultContent, status)
}

// appendControlToolResult records the engine's answer to a control tool call
// (complete, escalate): the tool result message, its persisted record, and the
// card all carry the same explicit status.
func (s *SubAgent) appendControlToolResult(callID, name, argsJSON, resultContent string, status ToolResultStatus) {
	if strings.TrimSpace(callID) == "" {
		return
	}
	toolMsg := message.Message{
		Role:       "tool",
		ToolCallID: callID,
		Content:    resultContent,
		ToolStatus: string(status),
	}
	s.ctxMgr.Append(toolMsg)
	s.persistMessageAsync(toolMsg, name+" tool result", nil)
	s.turn.removeStreamingToolCall(callID)
	event := ToolResultEvent{
		CallID:   callID,
		Name:     name,
		ArgsJSON: argsJSON,
		Result:   resultContent,
		Status:   status,
		AgentID:  s.instanceID,
	}
	s.parent.emitToTUI(event)
}

func (s *SubAgent) StateChangedAt() time.Time {
	_, changedAt := s.runtimeState.artifactSnapshot()
	return changedAt
}

func (s *SubAgent) setState(state SubAgentState, summary string) bool {
	if !s.runtimeState.set(state, summary) {
		// A rejected transition is an invariant violation in the coordination
		// layer, not a user-visible failure: surface it loudly instead of
		// silently dropping the write.
		if s.parent != nil {
			s.parent.orchestrationMetrics.recordRejectedStateTransition(s.State(), state)
		}
		log.Warnf("sub-agent state transition rejected agent=%v from=%q to=%q", s.instanceID, s.State(), state)
		return false
	}
	if state == SubAgentStateRunning {
		s.signalWake()
	}
	return true
}

// setStateFrom is the SubAgent-level compare-and-transition wrapper used by a
// guarded terminal commit (see commitTerminalTaskFrom): it only moves the
// runtime from the expected state and reports failure when a concurrent
// reactivation already moved it.
func (s *SubAgent) setStateFrom(from, to SubAgentState, summary string) bool {
	if s == nil {
		return false
	}
	if !s.runtimeState.setFrom(from, to, summary) {
		if s.parent != nil {
			s.parent.orchestrationMetrics.recordRejectedStateTransition(s.State(), to)
		}
		log.Warnf("sub-agent state transition rejected agent=%v expected_from=%q actual=%q to=%q", s.instanceID, from, s.State(), to)
		return false
	}
	if to == SubAgentStateRunning {
		s.signalWake()
	}
	return true
}

func (s *SubAgent) restoreState(state SubAgentState, summary string) {
	s.runtimeState.restore(state, summary)
}

func (s *SubAgent) updateProgress(summary string) bool {
	return s.runtimeState.updateProgress(summary)
}

func (s *SubAgent) resetForAttempt(summary string) bool {
	return s.runtimeState.resetForAttempt(summary)
}

func (s *SubAgent) rollbackState(expected, state SubAgentState, summary string) bool {
	return s.runtimeState.rollback(expected, state, summary)
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

// currentWriteScope returns the task record's advisory write-scope snapshot.
// The declared scope is advisory and never gates tool execution: file access
// is decided by the role's permission rules. The snapshot only feeds record
// sync, context summaries, and later overlap advice. It is read under the lock
// because activation publication can replace it while the worker runs.
func (s *SubAgent) currentWriteScope() tools.WriteScope {
	s.writeScopeMu.RLock()
	defer s.writeScopeMu.RUnlock()
	return s.writeScope.Normalized()
}

// publishWriteScope replaces the live scope snapshot with a committed record
// scope. Activation publication runs outside the worker's turn, so the live
// value must track the record the activation was committed from; the scope is
// advisory for execution and never gates the worker's writes.
func (s *SubAgent) publishWriteScope(scope tools.WriteScope) tools.WriteScope {
	s.writeScopeMu.Lock()
	defer s.writeScopeMu.Unlock()
	s.writeScope = scope.Normalized()
	return s.writeScope
}
