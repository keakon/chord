package agent

import (
	"fmt"
	"sync"
	"time"

	"github.com/keakon/chord/internal/tools"
)

type SubAgentState string

const (
	SubAgentStateRunning           SubAgentState = "running"
	SubAgentStateWaitingMain       SubAgentState = "waiting_main"
	SubAgentStateWaitingDescendant SubAgentState = "waiting_descendant"
	SubAgentStateCompleted         SubAgentState = "completed"
	SubAgentStateFailed            SubAgentState = "failed"
	SubAgentStateCancelled         SubAgentState = "cancelled"
	SubAgentStateIdle              SubAgentState = "idle"
)

func normalizeSubAgentState(state SubAgentState) SubAgentState {
	return state
}

func isKnownSubAgentState(state SubAgentState) bool {
	switch state {
	case SubAgentStateRunning, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant,
		SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled, SubAgentStateIdle:
		return true
	default:
		return false
	}
}

// validSubAgentStateTransition is the single authority for sub-agent runtime
// state changes. Rules:
//
//   - An empty state may bootstrap into any non-empty state (tests and
//     standalone sub-agents construct records directly at terminal states).
//   - Re-setting the current state is idempotent.
//   - A terminal state is absorbing for ordinary events. Reuse of a terminal
//     runtime is an explicit new-attempt operation, not a normal transition.
//   - All non-terminal states may move to any real state; unknown states are
//     rejected unless already current.
func validSubAgentStateTransition(from, to SubAgentState) bool {
	if from == "" {
		return isKnownSubAgentState(to)
	}
	if !isKnownSubAgentState(from) || !isKnownSubAgentState(to) {
		return false
	}
	if from == to {
		return true
	}
	if from == SubAgentStateCompleted || from == SubAgentStateFailed || from == SubAgentStateCancelled {
		return to == SubAgentStateRunning
	}
	switch to {
	case SubAgentStateRunning, SubAgentStateIdle, SubAgentStateWaitingMain, SubAgentStateWaitingDescendant, SubAgentStateCompleted, SubAgentStateFailed, SubAgentStateCancelled:
		return true
	default:
		return false
	}
}

func validateSubAgentStateTransition(from, to SubAgentState) error {
	if validSubAgentStateTransition(from, to) {
		return nil
	}
	return fmt.Errorf("invalid sub-agent state transition: %q -> %q", from, to)
}

type subAgentRuntimeState struct {
	mu                   sync.RWMutex
	state                SubAgentState
	lastSummary          string
	lastMailboxID        string
	lastReplyMessageID   string
	lastReplyToMailboxID string
	lastReplyKind        string
	lastReplySummary     string
	lastArtifact         tools.ArtifactRef
	pendingComplete      *AgentResult
	// stateChangedAt is the worker's activity metric: it is refreshed both on
	// state transitions (set) and on real progress (markActivity), so the
	// coordination layer can tell a busy worker that merely stays in Running
	// from one that has produced no state change and no activity for a long
	// wall-clock window. WaitingMain expiry reads the same clock as its
	// wait-since anchor; waiting workers do not generate activity, so the two
	// uses stay coherent.
	stateChangedAt time.Time
	// stallAlertRaised deduplicates the owner-facing stall risk alert: the
	// periodic lifecycle sweep notifies once per stall episode and clears the
	// flag when the worker shows activity again.
	stallAlertRaised bool
}

func (s *subAgentRuntimeState) set(state SubAgentState, summary string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateSubAgentStateTransition(s.state, state); err != nil {
		return false
	}
	s.state = state
	s.stateChangedAt = time.Now()
	if summary != "" {
		s.lastSummary = summary
	}
	return true
}

func (s *subAgentRuntimeState) restore(state SubAgentState, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	s.stateChangedAt = time.Now()
	if summary != "" {
		s.lastSummary = summary
	}
}

func (s *subAgentRuntimeState) resetForAttempt(summary string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !isTerminalSubAgentState(s.state) {
		return false
	}
	s.state = SubAgentStateIdle
	s.stateChangedAt = time.Now()
	if summary != "" {
		s.lastSummary = summary
	}
	return true
}

// markActivity refreshes the activity metric on real worker progress (LLM
// request issue, stream deltas, tool results, response handling). It is the
// heartbeat the coordination stall detection reads; state transitions alone
// were too coarse and mislabelled long-running but busy workers as stalled.
func (s *subAgentRuntimeState) markActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateChangedAt = time.Now()
}

func (s *subAgentRuntimeState) stallAlerted() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stallAlertRaised
}

func (s *subAgentRuntimeState) setStallAlerted(raised bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stallAlertRaised = raised
}

func (s *subAgentRuntimeState) setPendingComplete(result *AgentResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingComplete = cloneAgentResult(result)
}

func (s *subAgentRuntimeState) clearPendingComplete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingComplete = nil
}

func (s *subAgentRuntimeState) pendingCompleteSnapshot() *AgentResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneAgentResult(s.pendingComplete)
}

func (s *subAgentRuntimeState) snapshot() (SubAgentState, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state, s.lastSummary
}

func (s *subAgentRuntimeState) setLastMailboxID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastMailboxID = id
}

func (s *subAgentRuntimeState) setReplyThread(replyMessageID, replyToMailboxID, replyKind, replySummary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReplyMessageID = replyMessageID
	s.lastReplyToMailboxID = replyToMailboxID
	s.lastReplyKind = replyKind
	if replySummary != "" {
		s.lastReplySummary = replySummary
	}
}

func (s *subAgentRuntimeState) mailboxThreadSnapshot() (lastMailboxID, lastReplyMessageID, lastReplyToMailboxID, lastReplyKind, lastReplySummary string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastMailboxID, s.lastReplyMessageID, s.lastReplyToMailboxID, s.lastReplyKind, s.lastReplySummary
}

func (s *subAgentRuntimeState) setLastArtifact(ref tools.ArtifactRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastArtifact = tools.NormalizeArtifactRef(ref)
}

func (s *subAgentRuntimeState) artifactSnapshot() (tools.ArtifactRef, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastArtifact, s.stateChangedAt
}

// markActivity records real worker progress so coordination stall detection
// stops mislabelling long-running but active workers (see
// subAgentRuntimeState.stateChangedAt).
func (s *SubAgent) markActivity() {
	if s == nil {
		return
	}
	s.runtimeState.markActivity()
}

// stallAlertRaised reports whether the main-side lifecycle sweep already
// notified this worker's owner about its current stall episode.
func (s *SubAgent) stallAlertRaised() bool {
	if s == nil {
		return false
	}
	return s.runtimeState.stallAlerted()
}

func (s *SubAgent) raiseStallAlert() {
	if s == nil {
		return
	}
	s.runtimeState.setStallAlerted(true)
}

func (s *SubAgent) clearStallAlert() {
	if s == nil {
		return
	}
	s.runtimeState.setStallAlerted(false)
}
