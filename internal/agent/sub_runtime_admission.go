package agent

import (
	"os"
	"time"
)

// This file adapts *SubAgent to the runtime construction and completion
// surface shared with external backends. Keep it thin: the behavior lives in
// the existing SubAgent paths.

func (s *SubAgent) OwnerSnapshot() (ownerAgentID, ownerTaskID string, depth int, joinToOwner bool) {
	return s.ownerSnapshot()
}

func (s *SubAgent) TaskDescription() string { return s.taskDesc }

func (s *SubAgent) ColorCode() string { return s.color }

func (s *SubAgent) LastReplyMessageID() string {
	replyMessageID, _, _, _ := s.LastReplyThread()
	return replyMessageID
}

func (s *SubAgent) injectInitialInput(description string) bool {
	return s.InjectAdmittedInitialInput(description)
}

func (s *SubAgent) removeInitialInput() {
	s.removeInitialUserMessage()
}

func (s *SubAgent) buildTaskRecord(previous *DurableTaskRecord, closedReason string, currentTurn uint64, now time.Time) *DurableTaskRecord {
	return buildTaskRecordFromSub(s, previous, closedReason, currentTurn, now)
}

func (s *SubAgent) persistRegistration(sessionDir string, record *DurableTaskRecord) error {
	if s == nil || s.parent == nil {
		return nil
	}
	return s.parent.persistSubAgentRegistration(sessionDir, s, record)
}

func (s *SubAgent) removeRegistration(sessionDir, taskID string) {
	if s == nil || s.parent == nil {
		return
	}
	_ = os.Remove(subAgentMetaPath(sessionDir, s.instanceID))
	_ = s.parent.persistTaskRegistryRecord(sessionDir, taskID, nil)
}

func (s *SubAgent) persistRecoverySnapshot(taskID string) {
	if s == nil || s.parent == nil {
		return
	}
	s.parent.persistSubAgentRecoverySnapshot(s, taskID)
}

func (s *SubAgent) takeSlot() {
	s.semMu.Lock()
	s.semHeld = true
	s.semMu.Unlock()
}

func (s *SubAgent) releaseSlot() (held, borrowed, bypassed bool) {
	s.semMu.Lock()
	defer s.semMu.Unlock()
	held, borrowed, bypassed = s.semHeld, s.semBorrowed, s.semBypassed
	s.semHeld = false
	s.semBorrowed = false
	s.semBypassed = false
	return held, borrowed, bypassed
}

func (s *SubAgent) slotHeld() bool {
	held, _ := s.slotState()
	return held
}

func (s *SubAgent) startRuntime() {
	s.startRunLoop()
	s.armStartupWatchdog()
}

func (s *SubAgent) abortStartup() {
	if s == nil {
		return
	}
	s.cancel()
}
