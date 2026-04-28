package agent

import (
	"sync"
	"time"
)

type SubAgentState string

const (
	SubAgentStateRunning           SubAgentState = "running"
	SubAgentStateWaitingPrimary    SubAgentState = "waiting_primary"
	SubAgentStateWaitingDescendant SubAgentState = "waiting_descendant"
	SubAgentStateCompleted         SubAgentState = "completed"
	SubAgentStateFailed            SubAgentState = "failed"
	SubAgentStateCancelled         SubAgentState = "cancelled"
	SubAgentStateIdle              SubAgentState = "idle"
)

type subAgentRuntimeState struct {
	mu                     sync.RWMutex
	state                  SubAgentState
	lastSummary            string
	lastMailboxID          string
	lastReplyMessageID     string
	lastReplyToMailboxID   string
	lastReplyKind          string
	lastReplySummary       string
	lastArtifactID         string
	lastArtifactRelPath    string
	lastArtifactType       string
	pendingCompleteIntent  bool
	pendingCompleteSummary string
	stateChangedAt         time.Time
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

func (s *subAgentRuntimeState) setPendingComplete(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingCompleteIntent = true
	s.pendingCompleteSummary = summary
}

func (s *subAgentRuntimeState) clearPendingComplete() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingCompleteIntent = false
	s.pendingCompleteSummary = ""
}

func (s *subAgentRuntimeState) pendingCompleteSnapshot() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingCompleteIntent, s.pendingCompleteSummary
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

func (s *subAgentRuntimeState) setLastArtifact(artifactID, artifactRelPath, artifactType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastArtifactID = artifactID
	s.lastArtifactRelPath = artifactRelPath
	s.lastArtifactType = artifactType
}

func (s *subAgentRuntimeState) artifactSnapshot() (artifactID, artifactRelPath, artifactType string, stateChangedAt time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastArtifactID, s.lastArtifactRelPath, s.lastArtifactType, s.stateChangedAt
}
