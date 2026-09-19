package agent

import (
	"fmt"
	"strings"
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
//   - A terminal state is absorbing: no ordinary transition leaves it. Reuse
//     of a terminal runtime is an explicit new-attempt operation that first
//     resets the runtime to Idle (resetForAttempt, always paired with the
//     task-record attempt bump), so a late or manual delivery can never
//     resurrect a settled attempt by flipping it straight back to Running.
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
	if isTerminalSubAgentState(from) {
		return false
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

// setFrom transitions to state only when the runtime is still in from. The
// compare-and-transition runs under the state lock, so a caller that requires
// the transition to start from one specific state (a guarded terminal commit)
// cannot be beaten by a concurrent reactivation that moved the runtime first.
func (s *subAgentRuntimeState) setFrom(from, to SubAgentState, summary string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != from {
		return false
	}
	if err := validateSubAgentStateTransition(from, to); err != nil {
		return false
	}
	s.state = to
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

func (s *subAgentRuntimeState) updateProgress(summary string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != SubAgentStateRunning {
		return false
	}
	s.lastSummary = summary
	s.stateChangedAt = time.Now()
	return true
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

func (s *subAgentRuntimeState) rollback(expected, state SubAgentState, summary string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != expected || !isKnownSubAgentState(state) {
		return false
	}
	s.state = state
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
	// Real progress ends any cooling wait this request was granted: the
	// heartbeat is fresh again, so the grace window must not keep extending
	// the liveness deadlines for a request that later wedges. Every request
	// boundary (issue, stream progress, response, tool result) lands here.
	s.clearLLMCoolingWait()
}

// noteLLMCoolingWait records the end of a bounded cooling wait reported by the
// LLM client, or clears it when the wait is over. A cooling request is silent
// on purpose — the client is sleeping out a key cooldown that can legitimately
// run far past the silence budget — so the liveness checks extend their
// deadline instead of cancelling a healthy request.
func (s *SubAgent) noteLLMCoolingWait(deadline time.Time) {
	if s == nil {
		return
	}
	if deadline.IsZero() || !deadline.After(time.Now()) {
		s.llmCoolingDeadline.Store(0)
		return
	}
	s.llmCoolingDeadline.Store(deadline.UnixNano())
}

// clearLLMCoolingWait forgets any recorded cooling wait. Called at request
// boundaries so a finished request never leaves a stale grace window behind.
func (s *SubAgent) clearLLMCoolingWait() {
	if s == nil {
		return
	}
	s.llmCoolingDeadline.Store(0)
}

// llmCoolingWaitDeadline returns the recorded cooling deadline while it is
// still in the future, and the zero time otherwise.
func (s *SubAgent) llmCoolingWaitDeadline() time.Time {
	if s == nil {
		return time.Time{}
	}
	nanos := s.llmCoolingDeadline.Load()
	if nanos == 0 {
		return time.Time{}
	}
	deadline := time.Unix(0, nanos)
	if !deadline.After(time.Now()) {
		return time.Time{}
	}
	return deadline
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
