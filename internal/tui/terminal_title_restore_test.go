package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

// titleRestoreAgentStub overrides the two getters used by
// updateTerminalTitleFromRestoredSession; everything else delegates to the
// shared loopBusyAgentStub from oscnotify_test.go.
type titleRestoreAgentStub struct {
	loopBusyAgentStub
	summary  *agent.SessionSummary
	messages []message.Message
}

func (s titleRestoreAgentStub) GetSessionSummary() *agent.SessionSummary { return s.summary }
func (s titleRestoreAgentStub) GetMessages() []message.Message           { return s.messages }
func (s titleRestoreAgentStub) CanUseLoopMode() bool                     { return true }

func TestUpdateTerminalTitleFromRestoredSession_PrefersStoredOriginalWhenPresent(t *testing.T) {
	stub := titleRestoreAgentStub{
		summary: &agent.SessionSummary{
			OriginalFirstUserMessage: "fix login crash",
		},
		messages: []message.Message{
			{Role: "user", Content: "[Context Summary]\n## Goal\nrefactor", IsCompactionSummary: true},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "newer follow-up message"},
		},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	got := m.terminalTitleBase
	want := "fix login crash"
	if got != want {
		t.Fatalf("terminalTitleBase = %q, want %q", got, want)
	}
}

func TestUpdateTerminalTitleFromRestoredSession_PrefersCustomTitle(t *testing.T) {
	stub := titleRestoreAgentStub{
		summary: &agent.SessionSummary{
			Title:                    "Custom roadmap",
			OriginalFirstUserMessage: "fix login crash",
		},
		messages: []message.Message{
			{Role: "user", Content: "fallback user request"},
		},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	if got := m.terminalTitleBase; got != "Custom roadmap" {
		t.Fatalf("terminalTitleBase = %q, want custom title", got)
	}
}

func TestUpdateTerminalTitleFromRestoredSession_AllowsUserTextMatchingCompactionHeader(t *testing.T) {
	stub := titleRestoreAgentStub{
		summary: &agent.SessionSummary{
			OriginalFirstUserMessage: "[Context Summary]\n## Goal\nuser really typed this",
		},
		messages: []message.Message{
			{Role: "user", Content: "fallback user request"},
		},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	got := m.terminalTitleBase
	want := "[Context Summary] ## Goal user…"
	if got != want {
		t.Fatalf("terminalTitleBase = %q, want %q", got, want)
	}
}

func TestUpdateTerminalTitleFromRestoredSession_PrefersOriginalWhenClean(t *testing.T) {
	stub := titleRestoreAgentStub{
		summary: &agent.SessionSummary{
			OriginalFirstUserMessage: "fix login crash",
		},
		messages: []message.Message{
			{Role: "user", Content: "[Context Summary]\n…", IsCompactionSummary: true},
			{Role: "user", Content: "newer request"},
		},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	got := m.terminalTitleBase
	want := "fix login crash"
	if got != want {
		t.Fatalf("terminalTitleBase = %q, want %q", got, want)
	}
}

func TestUpdateTerminalTitleFromRestoredSession_SkipsSyntheticUserMessages(t *testing.T) {
	stub := titleRestoreAgentStub{
		messages: []message.Message{
			{Role: message.RoleUser, Content: "mailbox", Kind: message.KindSubAgentMailbox, Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1"}},
			{Role: message.RoleUser, Content: "loop", Kind: message.KindLoopNotice},
			{Role: message.RoleUser, Content: "real request"},
		},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	if got := m.terminalTitleBase; got != "real request" {
		t.Fatalf("terminalTitleBase = %q, want real request", got)
	}
}

func TestUpdateTerminalTitleFromRestoredSession_DoesNotUseMailboxAsLastFallback(t *testing.T) {
	stub := titleRestoreAgentStub{
		messages: []message.Message{{
			Role:    message.RoleUser,
			Content: "mailbox",
			Kind:    message.KindSubAgentMailbox,
			Mailbox: &message.MailboxMetadata{MessageID: "worker-1-1"},
		}},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	if got := m.terminalTitleBase; got != "" {
		t.Fatalf("terminalTitleBase = %q, want default empty title", got)
	}
}
