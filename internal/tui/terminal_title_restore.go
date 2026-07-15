package tui

import "github.com/keakon/chord/internal/message"

// updateTerminalTitleFromRestoredSession extracts the custom title or first
// user message from the restored session and updates the terminal title. It is called after
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

	// Prefer OriginalFirstUserMessage from the session summary when metadata says
	// it is user-authored. Do not infer compaction from text content: a real user
	// may legitimately start a prompt with the compaction header text.
	if summary := m.agent.GetSessionSummary(); summary != nil {
		if summary.Title != "" {
			m.setTerminalTitleFromMessage(summary.Title)
			return
		}
		if summary.OriginalFirstUserMessage != "" {
			m.setTerminalTitleFromMessage(summary.OriginalFirstUserMessage)
			return
		}
	}

	// Fallback: scan messages for the first non-compaction-summary user message.
	msgs := m.agent.GetMessages()
	for _, msg := range msgs {
		if !message.IsUserAuthored(msg) {
			continue
		}
		content := message.UserPromptPlainText(msg)
		if content == "" {
			continue
		}
		m.setTerminalTitleFromMessage(content)
		return
	}

	// Last fallback: use the very first non-mailbox user-role message. This may
	// be a compaction summary when no original user prompt remains available.
	for _, msg := range msgs {
		if msg.Role != message.RoleUser || msg.Kind == message.KindSubAgentMailbox || msg.Kind == message.KindLoopNotice {
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
