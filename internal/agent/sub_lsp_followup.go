package agent

import (
	"github.com/keakon/chord/internal/message"
)

// SetLSPFollowupSource installs the source of late LSP diagnostics for this
// worker. The source is shared with the parent but filters by writerAgentID,
// so a worker only sees late diagnostics for files it wrote.
func (s *SubAgent) SetLSPFollowupSource(source LSPFollowupSource) {
	if s == nil {
		return
	}
	s.lspFollowupSource = source
}

// buildLSPFollowupOverlay stages and returns the request-tail message carrying
// late diagnostics for files this worker recently wrote. The follow-up is
// persisted only after the request actually starts, so a cancelled request
// leaves the diagnostics pending for the next attempt.
func (s *SubAgent) buildLSPFollowupOverlay() (message.Message, bool) {
	if s == nil {
		return message.Message{}, false
	}
	s.stagedLSPFollowupMu.Lock()
	s.stagedLSPFollowup = nil
	s.stagedLSPFollowupMu.Unlock()
	if s.lspFollowupSource == nil || s.ctxMgr == nil {
		return message.Message{}, false
	}
	followup := pendingLSPFollowup(s.lspFollowupSource, s.ctxMgr.ScanBackward, s.effectiveToolBaseDir(), s.instanceID)
	if followup == nil {
		return message.Message{}, false
	}
	s.stagedLSPFollowupMu.Lock()
	s.stagedLSPFollowup = followup
	s.stagedLSPFollowupMu.Unlock()
	return followup.overlay(), true
}

// emitStagedLSPFollowup persists the follow-up the dispatched request carried
// and surfaces it as a card backed by that message. The model has already
// received the text, so the source is acknowledged as soon as the row is
// appended; a persistence failure defers the live card until recovery.
func (s *SubAgent) emitStagedLSPFollowup() {
	if s == nil || s.ctxMgr == nil {
		return
	}
	s.stagedLSPFollowupMu.Lock()
	staged := s.stagedLSPFollowup
	s.stagedLSPFollowup = nil
	s.stagedLSPFollowupMu.Unlock()
	if staged == nil {
		return
	}
	var reviews []message.LSPReview
	for _, item := range staged.items {
		reviews = append(reviews, item.Reviews...)
	}
	msg := message.Message{
		Role:       message.RoleUser,
		Kind:       message.KindLSPFollowup,
		Content:    message.SystemReminderOverlayText(staged.text),
		LSPReviews: reviews,
	}
	messageIndex := s.ctxMgr.MessageCount()
	s.ctxMgr.Append(msg)
	ackLSPFollowupItems(staged.items)
	s.deferLSPFollowup(messageIndex)
	s.persistMessageAsync(msg, "LSP follow-up", func() {
		s.flushDeferredLSPFollowupsThrough(messageIndex)
	})
}

func (s *SubAgent) deferLSPFollowup(index int) {
	s.deferredLSPFollowupMu.Lock()
	if !s.deferredLSPFollowup || index < s.deferredLSPFollowupFrom {
		s.deferredLSPFollowupFrom = index
	}
	s.deferredLSPFollowup = true
	s.deferredLSPFollowupMu.Unlock()
}

func (s *SubAgent) flushDeferredLSPFollowups() {
	s.flushDeferredLSPFollowupsThrough(s.ctxMgr.MessageCount() - 1)
}

// Successful persistence of a later row does not establish that earlier rows
// succeeded. Only a checkpoint can release the backlog after degradation.
func (s *SubAgent) flushDeferredLSPFollowupsThrough(index int) {
	s.deferredLSPFollowupMu.Lock()
	defer s.deferredLSPFollowupMu.Unlock()
	if !s.deferredLSPFollowup || index < s.deferredLSPFollowupFrom {
		return
	}
	if s.parent == nil || s.recoveryManager() == nil {
		return
	}
	if s.PersistenceHealth().State == PersistenceDegraded {
		return
	}
	from := s.deferredLSPFollowupFrom
	for offset, msg := range s.ctxMgr.SnapshotFrom(from) {
		messageIndex := from + offset
		if messageIndex > index {
			break
		}
		if msg.Kind == message.KindLSPFollowup {
			s.parent.emitToTUI(LSPFollowupEvent{Message: message.StripSystemReminderOverlay(msg.Content), MessageIndex: messageIndex, AgentID: s.instanceID})
		}
	}
	if index >= s.ctxMgr.MessageCount()-1 {
		s.deferredLSPFollowup = false
	} else {
		s.deferredLSPFollowupFrom = index + 1
	}
}
