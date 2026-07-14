package agent

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/keakon/chord/internal/message"
)

// SessionEnvSnapshot carries the session-stable environment fields previously
// rendered inside the system prompt's <env> block. Moving them into the
// session-context reminder (injected before the first user message) keeps the
// system prompt fully static across sessions, days, and working directories,
// which is the prefix prompt caching depends on.
type SessionEnvSnapshot struct {
	WorkDir  string
	Platform string
	VenvRel  string
	Date     string
}

// hasEnv reports whether the snapshot carries any environment field.
func (e SessionEnvSnapshot) hasEnv() bool {
	return e.WorkDir != "" || e.Platform != "" || e.VenvRel != "" || e.Date != ""
}

// renderEnvBlock renders the <env> block using the same format the system
// prompt previously embedded, so the model sees identical information before
// the first user message instead of inside the cached system prefix.
func (e SessionEnvSnapshot) renderEnvBlock() string {
	if !e.hasEnv() {
		return ""
	}
	workDir := e.WorkDir
	if workDir == "" {
		workDir = "unknown"
	}
	venvLine := ""
	if e.VenvRel != "" {
		venvLine = fmt.Sprintf("\n  Python virtual environment: %s\n  When running Python commands, prefer the interpreter from this virtual environment.", e.VenvRel)
	}
	return fmt.Sprintf(`<env>
  Working directory: %s
  Platform: %s
  Today's date: %s%s
</env>`, workDir, e.Platform, e.Date, venvLine)
}

// buildSessionContextReminder constructs a meta user message that carries
// session-level context (environment and AGENTS.md) to the model without
// polluting the stable system prompt or being persisted to ctxMgr/jsonl.
//
// Callers should treat an empty result as "no reminder to inject". When the
// environment snapshot and agentsMD are both empty, the reminder is skipped.
func buildSessionContextReminder(env SessionEnvSnapshot, agentsMD string) string {
	agentsMD = strings.TrimSpace(agentsMD)
	hasAgentsMD := agentsMD != ""
	hasEnvField := env.hasEnv()
	if !hasAgentsMD && !hasEnvField {
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
		sb.WriteString("Each applicable AGENTS.md from the repository root through the current working directory is already loaded here before the first visible user message, in root-to-current order and with its path labeled. ")
		sb.WriteString(agentsMDInstructionRequirement)
		sb.WriteString("\n\n")
		sb.WriteString(agentsMD)
		sb.WriteString("\n</INSTRUCTIONS>\n")
	}
	if envBlock := env.renderEnvBlock(); envBlock != "" {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(envBlock)
		sb.WriteByte('\n')
	}
	return strings.TrimSpace(sb.String())
}

// sessionEnvSnapshot captures the session-stable environment fields for the
// session-context reminder. The date uses the same "Mon Jan 2 2006" format the
// system prompt previously embedded.
func (a *MainAgent) sessionEnvSnapshot() SessionEnvSnapshot {
	workDir, _, _, venvPath := a.promptMetaSnapshot()
	venvRel := ""
	if venvPath != "" && workDir != "" {
		venvRel = displayPathFromWorkDir(workDir, venvPath)
	}
	return SessionEnvSnapshot{
		WorkDir:  workDir,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		VenvRel:  venvRel,
		Date:     time.Now().Format("Mon Jan 2 2006"),
	}
}

// refreshSessionContextReminder captures a snapshot of the current environment,
// AGENTS.md content, and date into the per-agent cached reminder. It is called
// from ensureSessionBuilt once all session surfaces are ready, and again after
// a session-head reset (e.g. /new, /resume, plan execution session, role switch
// with clearHistory=true).
func (a *MainAgent) refreshSessionContextReminder() {
	env := a.sessionEnvSnapshot()
	agentsMD := a.cachedAgentsMDSnapshot()
	content := buildSessionContextReminder(env, agentsMD)
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
