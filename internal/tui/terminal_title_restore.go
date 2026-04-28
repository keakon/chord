package tui

import (
	"github.com/keakon/chord/internal/message"
)

// updateTerminalTitleFromRestoredSession extracts the first user message from
// the restored session and updates the terminal title. It is called after
// sessionRestoredRebuildMsg.
//
// Compaction handling:
//   - Durable compaction replaces main.jsonl with summary + evidence + recentTail.
//   - OriginalFirstUserMessage is preserved in usage-summary.json and never
//     overwritten by compaction.
//   - If OriginalFirstUserMessage is unavailable, we fall back to scanning
//     GetMessages() for the first non-compaction-summary user message, then to
//     the very first user message (which may be the summary text).
func (m *Model) updateTerminalTitleFromRestoredSession() {
	if m.agent == nil {
		m.resetTerminalTitle()
		return
	}

	// Prefer OriginalFirstUserMessage from the session summary (preserved
	// across compaction).
	if summary := m.agent.GetSessionSummary(); summary != nil {
		if summary.OriginalFirstUserMessage != "" {
			m.setTerminalTitleFromMessage(summary.OriginalFirstUserMessage)
			return
		}
	}

	// Fallback: scan messages for the first non-compaction-summary user message.
	msgs := m.agent.GetMessages()
	for _, msg := range msgs {
		if msg.Role != "user" {
			continue
		}
		if msg.IsCompactionSummary {
			continue
		}
		content := message.UserPromptPlainText(msg)
		if content == "" {
			continue
		}
		m.setTerminalTitleFromMessage(content)
		return
	}

	// Last fallback: use the very first user message (may be compaction summary).
	for _, msg := range msgs {
		if msg.Role != "user" {
			continue
		}
		content := message.UserPromptPlainText(msg)
		if content == "" {
			continue
		}
		m.setTerminalTitleFromMessage(content)
		return
	}

	// No user message found; reset to default.
	m.resetTerminalTitle()
}
