package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
)

// buildSessionContextReminder constructs a meta user message that carries
// session-level context (AGENTS.md, current date) to the model without
// polluting the stable system prompt or being persisted to ctxMgr/jsonl.
//
// Callers should treat an empty result as "no reminder to inject". When
// agentsMD is empty and now is the zero value, the reminder is skipped.
func buildSessionContextReminder(agentsMD string, now time.Time) string {
	agentsMD = strings.TrimSpace(agentsMD)
	hasAgentsMD := agentsMD != ""
	hasDate := !now.IsZero()
	if !hasAgentsMD && !hasDate {
		return ""
	}

	var sb strings.Builder
	if hasAgentsMD {
		// AGENTS.md gets a self-identifying block: the "# AGENTS.md instructions"
		// header declares its identity on the first line, and <INSTRUCTIONS> markers
		// bound it so the model (and history filtering) can recognize it without
		// reading the whole preamble. Modeled on codex's contextual user fragment.
		sb.WriteString("# AGENTS.md instructions\n")
		sb.WriteString("<INSTRUCTIONS>\n")
		sb.WriteString("The following is the complete applicable AGENTS.md content for this workspace session. It was discovered by walking from the current working directory up to the project root, then injected in project-root-to-current-working-directory order. Each loaded section is labeled with its path relative to the current working directory. This is internal workspace guidance injected before the first real user message and may not appear in the visible transcript. These instructions are durable workspace context and system-provided guidance, not ordinary user content. Follow them unless they conflict with higher-priority system, developer, or user instructions.\n\n")
		sb.WriteString(agentsMD)
		sb.WriteString("\n</INSTRUCTIONS>\n")
	}
	if hasDate {
		sb.WriteString("\n# currentDate\n")
		fmt.Fprintf(&sb, "Today's date is %s.\n", now.Format("2006-01-02"))
	}
	return strings.TrimSpace(sb.String())
}

// refreshSessionContextReminder captures a snapshot of the current AGENTS.md
// content and date into the per-agent cached reminder. It is called from
// ensureSessionBuilt once all session surfaces are ready, and again after a
// session-head reset (e.g. /new, /resume, plan execution session, role switch with
// clearHistory=true).
func (a *MainAgent) refreshSessionContextReminder() {
	agentsMD := a.cachedAgentsMDSnapshot()
	content := buildSessionContextReminder(agentsMD, time.Now())
	if content == "" {
		a.cachedSessionReminderContent.Store(nil)
		return
	}
	a.cachedSessionReminderContent.Store(&content)
}

func (a *MainAgent) clearSessionContextReminder() {
	a.cachedSessionReminderContent.Store(nil)
}

// firstUserMessageIndex returns the index of the first user-role message in
// messages, or -1 if none exists. Used by injection helpers to find the
// insertion point for meta/overlay messages.
func firstUserMessageIndex(messages []message.Message) int {
	for i := range messages {
		if messages[i].Role == "user" {
			return i
		}
	}
	return -1
}

// injectMetaUserReminder prepends a meta user reminder block before the first
// user message (or at head if none). Returns a new slice when injected,
// otherwise returns the original slice.
func injectMetaUserReminder(messages []message.Message, content string) []message.Message {
	content = strings.TrimSpace(content)
	if content == "" {
		return messages
	}
	reminder := message.Message{Role: "user", Content: content}
	if len(messages) == 0 {
		return []message.Message{reminder}
	}
	insertAt := max(firstUserMessageIndex(messages), 0)
	out := make([]message.Message, 0, len(messages)+1)
	out = append(out, messages[:insertAt]...)
	out = append(out, reminder)
	out = append(out, messages[insertAt:]...)
	return out
}

// injectSessionContextReminder prepends the cached reminder (if any) before the
// first user message in messages.
//
// The reminder is meta: it is not stored in ctxMgr or persisted. It is injected
// once per session-head only.
func (a *MainAgent) injectSessionContextReminder(messages []message.Message) []message.Message {
	if a.sessionReminderInjected.Load() {
		return messages
	}
	ptr := a.cachedSessionReminderContent.Load()
	if ptr == nil {
		// No reminder for this session-head (e.g. AGENTS.md missing); treat as injected
		// so we don't re-check on every call.
		a.sessionReminderInjected.Store(true)
		return messages
	}
	out := injectMetaUserReminder(messages, *ptr)
	if len(out) != len(messages) {
		a.sessionReminderInjected.Store(true)
	}
	return out
}
