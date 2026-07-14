package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

func (s *SubAgent) handleUserInput(input pendingUserMessage) {
	if input.DrainContextAppends {
		s.drainContextAppendsBeforeTurn()
	}
	turn := s.newTurn()
	s.llmRequestInFlight.Store(true)
	s.appendPendingUserMessage(input)

	messages := s.ctxMgr.Snapshot()
	s.asyncCallLLMWithFlightMarked(turn, messages)
}

func (s *SubAgent) appendPendingUserMessage(input pendingUserMessage) {

	content := pendingUserMessageText(input)
	msg := message.Message{Role: "user", Content: content}
	if len(input.Parts) > 0 {
		msg.Parts = cloneContentParts(input.Parts)
	}
	msg.Content, msg.Parts = s.filterUnsupportedParts(msg.Content, msg.Parts)

	s.ctxMgr.Append(msg)
	if input.DraftID != "" {
		s.parent.emitToTUI(PendingDraftConsumedEvent{
			DraftID: input.DraftID,
			Parts:   messagePartsForTUI(msg),
			AgentID: s.instanceID,
		})
	}

	s.persistMessageAsync(msg, "user message", func() {
		if ackID := strings.TrimSpace(input.MailboxAckID); ackID != "" {
			s.parent.markSubAgentMailboxConsumed(ackID)
		}
	})
}

func (s *SubAgent) takePendingUserMessagesLocked() []pendingUserMessage {
	pending := make([]pendingUserMessage, 0, len(s.inputCh)+len(s.inputOverflow))
	for {
		select {
		case input := <-s.inputCh:
			pending = append(pending, input)
		default:
			pending = append(pending, s.inputOverflow...)
			s.inputOverflow = nil
			return pending
		}
	}
}

func (s *SubAgent) appendPendingUserMessages(pending []pendingUserMessage) {
	for _, input := range pending {
		if input.DrainContextAppends {
			s.drainContextAppendsBeforeTurn()
		}
		s.appendPendingUserMessage(input)
	}
}

func (s *SubAgent) messagesForLLMContinuation() []message.Message {
	s.inputQueueMu.Lock()
	pending := s.takePendingUserMessagesLocked()
	s.llmRequestInFlight.Store(true)
	s.inputQueueMu.Unlock()
	s.appendPendingUserMessages(pending)
	return s.ctxMgr.Snapshot()
}

func (s *SubAgent) continueLLMWithPendingUserMessages() {
	s.asyncCallLLMWithFlightMarked(s.turn, s.messagesForLLMContinuation())
}

func (s *SubAgent) continueLLMIfPendingUserMessages() bool {
	s.inputQueueMu.Lock()
	pending := s.takePendingUserMessagesLocked()
	if len(pending) == 0 {
		s.inputQueueMu.Unlock()
		return false
	}
	s.llmRequestInFlight.Store(true)
	s.inputQueueMu.Unlock()

	s.appendPendingUserMessages(pending)
	s.asyncCallLLMWithFlightMarked(s.turn, s.ctxMgr.Snapshot())
	return true
}

func (s *SubAgent) takePendingUserMessagesForContinuation() []pendingUserMessage {
	s.inputQueueMu.Lock()
	defer s.inputQueueMu.Unlock()

	pending := s.takePendingUserMessagesLocked()
	if len(pending) > 0 {
		s.llmRequestInFlight.Store(true)
	}
	return pending
}

func (s *SubAgent) drainContextAppendsBeforeTurn() {
	for {
		s.refillContextAppendChannelFromOverflow()
		pending, ok := s.tryReceiveContextAppend()
		if !ok {
			return
		}
		s.appendContextOnly(pending)
	}
}

func (s *SubAgent) TryEnqueueContextAppend(msg message.Message) bool {
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
		return true
	}
	msg.Role = "user"
	if err := s.parentCtx.Err(); err != nil {
		return false
	}
	s.ctxAppendQueueMu.Lock()
	defer s.ctxAppendQueueMu.Unlock()
	if len(s.ctxAppendOverflow) > 0 {
		s.ctxAppendOverflow = append(s.ctxAppendOverflow, msg)
		return true
	}
	select {
	case s.ctxAppendCh <- msg:
		return true
	default:
		s.ctxAppendOverflow = append(s.ctxAppendOverflow, msg)
		return true
	}
}

func (s *SubAgent) appendContextOnly(msg message.Message) {
	if strings.TrimSpace(msg.Content) == "" && len(msg.Parts) == 0 {
		return
	}
	ackID := strings.TrimSpace(msg.MailboxAckID)
	msg.Role = "user"
	s.ctxMgr.Append(msg)
	persistMsg := msg
	if strings.TrimSpace(persistMsg.Content) == "" {
		persistMsg.Content = message.UserPromptPlainText(msg)
	}
	s.persistMessageAsync(persistMsg, "context-append message", func() {
		if ackID != "" {
			s.parent.markSubAgentMailboxConsumed(ackID)
		}
	})
}

func (s *SubAgent) filterUnsupportedParts(content string, parts []message.ContentPart) (string, []message.ContentPart) {
	if len(parts) == 0 {
		return content, parts
	}
	llmClient, _ := s.llmSnapshot()
	if llmClient == nil {
		return content, parts
	}

	var filtered []message.ContentPart
	var dropped []string
	for _, p := range parts {
		switch p.Type {
		case "image":
			if !llmClient.SupportsInput("image") {
				dropped = append(dropped, "image")
				continue
			}
		case "pdf":
			if !llmClient.SupportsInput("pdf") {
				dropped = append(dropped, "pdf")
				continue
			}
		}
		filtered = append(filtered, p)
	}
	if len(dropped) > 0 {
		s.parent.emitToTUI(ToastEvent{
			Level:   "warn",
			Message: "Input dropped (unsupported): " + strings.Join(dropped, ", "),
			AgentID: s.instanceID,
		})
	}
	if len(filtered) == 0 {
		return content, nil
	}
	return content, filtered
}

func (s *SubAgent) toolResultParts(text string, images []message.ContentPart) []message.ContentPart {
	llmClient, _ := s.llmSnapshot()
	parts, dropped := toolResultPartsForCapability(text, images, llmClient)
	if dropped.any() {
		s.parent.emitToTUI(ToastEvent{
			Level:   "warn",
			Message: "Tool-result attachments dropped (unsupported): " + dropped.summary(),
			AgentID: s.instanceID,
		})
	}
	return parts
}
