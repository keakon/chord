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
	paths := recentWrittenPaths(s.ctxMgr.ScanBackward)
	if len(paths) == 0 {
		return message.Message{}, false
	}
	items := s.lspFollowupSource.Pending(paths, s.effectiveToolBaseDir(), s.instanceID)
	text := formatLSPFollowup(items)
	if text == "" {
		return message.Message{}, false
	}
	s.stagedLSPFollowupMu.Lock()
	s.stagedLSPFollowup = &lspFollowup{text: text, items: items}
	s.stagedLSPFollowupMu.Unlock()
	return message.Message{Role: message.RoleUser, Kind: message.KindTurnOverlay, Content: lspFollowupContent(text)}, true
}

// emitStagedLSPFollowup persists the follow-up the dispatched request carried
// and surfaces it as a card backed by that message. The model has already
// received the text, so the source is acknowledged as soon as the row is
// appended; a persistence failure only drops the live card.
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
		Content:    lspFollowupContent(staged.text),
		LSPReviews: reviews,
	}
	messageIndex := s.ctxMgr.MessageCount()
	s.ctxMgr.Append(msg)
	ackLSPFollowupItems(staged.items)
	text := staged.text
	s.persistMessageAsync(msg, "LSP follow-up", func() {
		if s.parent != nil {
			s.parent.emitToTUI(LSPFollowupEvent{Message: text, MessageIndex: messageIndex, AgentID: s.instanceID})
		}
	})
}
