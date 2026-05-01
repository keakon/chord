package agent

import (
	"github.com/keakon/golog/log"
	"strings"

	"github.com/keakon/chord/internal/message"
)

func (s *SubAgent) handleUserInput(input pendingUserMessage) {
	turn := s.newTurn()

	content := pendingUserMessageText(input)
	msg := message.Message{Role: "user", Content: content}
	if len(input.Parts) > 0 {
		msg.Parts = cloneContentParts(input.Parts)
	}
	msg.Content, msg.Parts = s.filterUnsupportedParts(msg.Content, msg.Parts)

	s.ctxMgr.Append(msg)

	go func() {
		if err := s.recovery.PersistMessage(s.instanceID, msg); err != nil {
			log.Warnf("SubAgent: failed to persist user message agent=%v error=%v", s.instanceID, err)
			return
		}
		if ackID := strings.TrimSpace(input.MailboxAckID); ackID != "" {
			s.parent.markSubAgentMailboxConsumed(ackID)
		}
	}()

	messages := s.ctxMgr.Snapshot()
	s.asyncCallLLM(turn, messages)
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
	go func() {
		persistMsg := msg
		if strings.TrimSpace(persistMsg.Content) == "" {
			persistMsg.Content = message.UserPromptPlainText(msg)
		}
		if err := s.recovery.PersistMessage(s.instanceID, persistMsg); err != nil {
			log.Warnf("SubAgent: failed to persist context-append message agent=%v error=%v", s.instanceID, err)
			return
		}
		if ackID != "" {
			s.parent.markSubAgentMailboxConsumed(ackID)
		}
	}()
}

func (s *SubAgent) filterUnsupportedParts(content string, parts []message.ContentPart) (string, []message.ContentPart) {
	if len(parts) == 0 {
		return content, parts
	}
	if s.llmClient == nil {
		return content, parts
	}

	var filtered []message.ContentPart
	var dropped []string
	for _, p := range parts {
		switch p.Type {
		case "image":
			if !s.llmClient.SupportsInput("image") {
				dropped = append(dropped, "image")
				continue
			}
		case "pdf":
			if !s.llmClient.SupportsInput("pdf") {
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
