package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

// buildTurnOverlayMessages assembles per-request meta user messages that
// deliver transient context — SubAgent mailbox, bug triage hint, loop
// continuation — without touching the stable system prompt or ctxMgr. Each
// returned message is wrapped as <system-reminder>...</system-reminder> and
// prepended to the real user turn by callLLM. None of them are persisted to
// the session jsonl.
func (a *MainAgent) buildTurnOverlayMessages() []message.Message {
	var overlays []message.Message

	if block := strings.TrimSpace(a.buildCoordinationSnapshotOverlay()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if msgs := a.takePendingSubAgentMailboxes(); len(msgs) > 0 {
		parts := make([]string, 0, len(msgs))
		for _, m := range msgs {
			if m == nil {
				continue
			}
			parts = append(parts, formatSubAgentMailboxInjectionText(m))
		}
		if len(parts) > 0 {
			overlays = append(overlays, message.Message{
				Role:    "user",
				Content: "<system-reminder>\n" + strings.Join(parts, "\n\n---\n\n") + "\n</system-reminder>",
			})
		}
	}

	if block := strings.TrimSpace(a.bugTriagePromptBlock()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	if block := strings.TrimSpace(a.pendingLoopContinuationPromptBlock()); block != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + block + "\n</system-reminder>",
		})
	}

	// Recovery prompt from length-recovery auto compaction: it is request-scoped
	// overlay that should not persist to durable context.
	if recoveryPrompt := a.takePendingRecoveryPrompt(); recoveryPrompt != "" {
		overlays = append(overlays, message.Message{
			Role:    "user",
			Content: "<system-reminder>\n" + recoveryPrompt + "\n</system-reminder>",
		})
	}

	return overlays
}

// takePendingRecoveryPrompt consumes the pending recovery prompt (one-shot).
// Returns the pending prompt if any, or empty string if none.
func (a *MainAgent) takePendingRecoveryPrompt() string {
	if a.pendingRecoveryPrompt == "" {
		return ""
	}
	prompt := a.pendingRecoveryPrompt
	a.pendingRecoveryPrompt = ""
	return prompt
}

// injectTurnOverlays prepends the turn overlays before the first user message
// (or at the head if no user message exists). Overlays are meta — they are not
// stored in ctxMgr or persisted. Returns a new slice when overlays are
// injected, otherwise the original slice unchanged.
func injectTurnOverlays(messages []message.Message, overlays []message.Message) []message.Message {
	if len(overlays) == 0 {
		return messages
	}
	insertAt := max(firstUserMessageIndex(messages), 0)
	out := make([]message.Message, 0, len(messages)+len(overlays))
	out = append(out, messages[:insertAt]...)
	out = append(out, overlays...)
	out = append(out, messages[insertAt:]...)
	return out
}
