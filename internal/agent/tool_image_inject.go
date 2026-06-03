package agent

import (
	"fmt"

	"github.com/keakon/chord/internal/message"
)

// buildToolImageMessage builds a synthetic user message that carries image
// content parts produced by tools during a tool-call batch. The leading text
// part keeps the message human-readable in history and gives the model a cue
// that the images originate from tool output.
func buildToolImageMessage(parts []message.ContentPart) message.Message {
	text := fmt.Sprintf("Loaded %d image(s) returned by tool calls.", len(parts))
	full := make([]message.ContentPart, 0, len(parts)+1)
	full = append(full, message.ContentPart{Type: "text", Text: text})
	full = append(full, parts...)
	return message.Message{Role: "user", Content: text, Parts: full}
}

// injectBatchToolImages appends the accumulated tool image parts as a synthetic
// user message before the next LLM call. Images are dropped (with a warning
// toast) when the active model does not accept image input, so the provider
// never receives an unsupported payload.
func (a *MainAgent) injectBatchToolImages() {
	if a.turn == nil || len(a.turn.batchImageParts) == 0 {
		return
	}
	parts := a.turn.batchImageParts
	a.turn.batchImageParts = nil

	a.llmMu.RLock()
	client := a.llmClient
	a.llmMu.RUnlock()
	if client == nil || !client.SupportsInput("image") {
		a.emitToTUI(ToastEvent{
			Message: "The current model does not support image input; tool images were dropped",
			Level:   "warn",
		})
		return
	}

	msg := buildToolImageMessage(parts)
	a.ctxMgr.Append(msg)
	if a.recovery != nil {
		a.persistAsync("main", msg)
	}
	a.emitToTUI(ToastEvent{
		Message: fmt.Sprintf("Loaded %d tool image(s) into context", len(parts)),
		Level:   "info",
	})
}

// injectBatchToolImages is the SubAgent counterpart of the main-agent method.
func (s *SubAgent) injectBatchToolImages() {
	if s.turn == nil || len(s.turn.batchImageParts) == 0 {
		return
	}
	parts := s.turn.batchImageParts
	s.turn.batchImageParts = nil

	llmClient, _ := s.llmSnapshot()
	if llmClient == nil || !llmClient.SupportsInput("image") {
		s.parent.emitToTUI(ToastEvent{
			Message: "The current model does not support image input; tool images were dropped",
			Level:   "warn",
			AgentID: s.instanceID,
		})
		return
	}

	msg := buildToolImageMessage(parts)
	s.ctxMgr.Append(msg)
	go func() {
		if s.recovery != nil {
			_ = s.recovery.PersistMessage(s.instanceID, msg)
		}
	}()
}
