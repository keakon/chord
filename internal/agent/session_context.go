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
// session-context reminder (injected before the first user message) keeps
// working directory, platform, date, and venv out of the cached system prefix.
// The system prompt itself is cache-stable framing: it still varies with
// Memory load and tool visibility, not with environment or time.
type SessionEnvSnapshot struct {
	WorkDir string
	// WorktreeName and WorktreeBranch identify the chord-managed worktree the
	// agent is currently working in, empty in a main checkout. They are part
	// of the environment block so the model can tell which checkout its
	// relative paths belong to.
	WorktreeName   string
	WorktreeBranch string
	// Git is the preformatted repository line from getGitStatus, empty until
	// the async lookup finished (and for SubAgents, which do not carry it).
	Git      string
	Platform string
	VenvRel  string
	Date     string
}

// hasEnv reports whether the snapshot carries any environment field.
func (e SessionEnvSnapshot) hasEnv() bool {
	return e.WorkDir != "" || e.Platform != "" || e.VenvRel != "" || e.Date != "" || e.WorktreeName != "" || e.Git != ""
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
	worktreeLine := ""
	if e.WorktreeName != "" {
		worktreeLine = fmt.Sprintf("\n  Worktree: %s", e.WorktreeName)
		if e.WorktreeBranch != "" {
			worktreeLine += fmt.Sprintf(" (branch %s)", e.WorktreeBranch)
		}
		worktreeLine += "\n  This is a separate checkout: relative paths resolve inside it, and the main checkout is a different directory."
	}
	gitLine := ""
	if e.Git != "" {
		gitLine = "\n  " + e.Git
	}
	venvLine := ""
	if e.VenvRel != "" {
		venvLine = fmt.Sprintf("\n  Python virtual environment: %s\n  When running Python commands, prefer the interpreter from this virtual environment.", e.VenvRel)
	}
	return fmt.Sprintf(`<env>
  Working directory: %s%s%s
  Platform: %s
  Today's date: %s%s
</env>`, workDir, worktreeLine, gitLine, e.Platform, e.Date, venvLine)
}

// buildSessionContextReminder constructs a meta user message that carries
// session-level context (environment and AGENTS.md) to the model without
// polluting the stable system prompt or being persisted to ctxMgr/jsonl.
//
// Callers should treat an empty result as "no reminder to inject". When the
// environment snapshot and agentsMD are both empty, the reminder is skipped.
func buildSessionContextReminder(env SessionEnvSnapshot, facts []message.DirectoryInstructionFact) string {
	agentsMD := strings.TrimSpace(message.RenderDirectoryInstructionFacts(facts))
	hasAgentsMD := agentsMD != ""
	hasEnvField := env.hasEnv()
	if !hasAgentsMD && !hasEnvField {
		return ""
	}

	var sb strings.Builder
	if hasAgentsMD {
		sb.WriteString(agentsMD)
		sb.WriteByte('\n')
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
	workDir, gitStatus, venvPath := a.promptMetaSnapshot()
	venvRel := ""
	if venvPath != "" && workDir != "" {
		venvRel = displayPathFromWorkDir(workDir, venvPath)
	}
	state := a.workDirState.load()
	return SessionEnvSnapshot{
		WorkDir:        workDir,
		WorktreeName:   state.WorktreeID,
		WorktreeBranch: state.Branch,
		Git:            strings.TrimSpace(gitStatus),
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		VenvRel:        venvRel,
		Date:           time.Now().Format("Mon Jan 2 2006"),
	}
}

// refreshSessionContextReminder captures a snapshot of the current environment,
// AGENTS.md content, and date into the per-agent cached reminder. It is called
// from ensureSessionBuilt once all session surfaces are ready, and again after
// a session-head reset (e.g. /new, /resume, plan execution session, role switch
// with clearHistory=true).
func (a *MainAgent) refreshSessionContextReminder() {
	env := a.sessionEnvSnapshot()
	content := buildSessionContextReminder(env, nil)
	// Memory is appended under its own untrusted wrapper (not inside the
	// AGENTS.md <INSTRUCTIONS> block). It reuses the same head-refresh /
	// per-request-inject lifecycle; a background commit lands only once
	// applyLoadedMemory takes it at a prompt-cache break.
	if mem := a.memoryReminderBlock(); mem != nil {
		if content != "" {
			content += "\n\n" + *mem
		} else {
			content = *mem
		}
	}
	if content == "" {
		a.cachedSessionReminderContent.Store(nil)
		return
	}
	a.cachedSessionReminderContent.Store(&content)
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
	reminder := message.Message{DiagnosticSource: message.DiagnosticSourceInstructions, Role: "user", Content: content}
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
// into every request so the prompt prefix keeps one stable shape. It was
// previously injected once per session-head, but the reminder never persists,
// so the very next request dropped it again and every session-head event
// (compaction apply, session restore) re-armed one more insert/remove pair —
// each flip rewriting the provider cache from position zero (observed seven
// times in one session). Steady re-injection costs a few cached KB per request
// and never invalidates the prefix; content changes only on refresh.
func (a *MainAgent) injectSessionContextReminder(messages []message.Message) []message.Message {
	ptr := a.cachedSessionReminderContent.Load()
	content := ""
	if ptr != nil {
		content = *ptr
	}
	return injectDirectoryInstructionReminder(messages, content, a.directoryInstructions.Load())
}

// sessionEnvSnapshot is the SubAgent counterpart of MainAgent.sessionEnvSnapshot.
// The working directory and worktree identity come from the live binding, so a
// worktree switch is reflected without rebuilding the agent.
func (s *SubAgent) sessionEnvSnapshot() SessionEnvSnapshot {
	workDir := s.effectiveToolBaseDir()
	venvRel := ""
	if s.venvPath != "" && workDir != "" {
		venvRel = displayPathFromWorkDir(workDir, s.venvPath)
	}
	state := s.workDirState.load()
	return SessionEnvSnapshot{
		WorkDir:        workDir,
		WorktreeName:   state.WorktreeID,
		WorktreeBranch: state.Branch,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		VenvRel:        venvRel,
		Date:           time.Now().Format("Mon Jan 2 2006"),
	}
}

// refreshSessionContextReminder rebuilds the SubAgent's cached reminder from
// the live environment and the current AGENTS.md. Construction is the
// SubAgent's only session head, but a worktree switch moves the working
// directory and may swap in the checkout's own AGENTS.md, so the reminder is
// rebuilt on that switch too: without it every later request would keep
// stating the checkout the worker left.
func (s *SubAgent) refreshSessionContextReminder() {
	content := buildSessionContextReminder(s.sessionEnvSnapshot(), nil)
	if content == "" {
		s.cachedSessionReminderContent.Store(nil)
		return
	}
	s.cachedSessionReminderContent.Store(&content)
}

func (s *SubAgent) initialDirectoryInstructionsText() string {
	return renderInitialDirectoryInstructions(s.directoryInstructions.Load())
}
func renderInitialDirectoryInstructions(r *directoryInstructionRuntime) string {
	if r != nil {
		if v := r.view.Load(); v != nil {
			return message.RenderDirectoryInstructionFacts(v.Initial)
		}
	}
	return ""
}

// injectSessionContextReminder is the SubAgent counterpart: same every-request
// contract as MainAgent, with content rebuilt from the live environment at
// construction and on every worktree switch instead of on session-head resets.
func (s *SubAgent) injectSessionContextReminder(messages []message.Message) []message.Message {
	ptr := s.cachedSessionReminderContent.Load()
	content := ""
	if ptr != nil {
		content = *ptr
	}
	return injectDirectoryInstructionReminder(messages, content, s.directoryInstructions.Load())
}

func injectDirectoryInstructionReminder(messages []message.Message, content string, r *directoryInstructionRuntime) []message.Message {
	if initial := renderInitialDirectoryInstructions(r); initial != "" {
		content = initial + "\n\n" + content
	}
	out := injectMetaUserReminder(messages, content)
	if r == nil || len(out) == len(messages) {
		return out
	}
	v := r.view.Load()
	if v == nil {
		return out
	}
	index := max(firstUserMessageIndex(messages), 0)
	out[index].Kind = message.KindDirectoryInstructionProjection
	for _, f := range v.Initial {
		out[index].DirectoryInstructionRefs = append(out[index].DirectoryInstructionRefs, f.Ref())
	}
	out[index].DirectoryInstructionDigest = message.DirectoryInstructionBodyHash(out[index].Content)
	return out
}
