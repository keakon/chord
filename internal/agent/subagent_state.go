package agent

import (
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

func (s *subAgentRuntimeState) set(state SubAgentState, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
	s.stateChangedAt = time.Now()
	if summary != "" {
		s.lastSummary = summary
	}
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
