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

func TestUpdateTerminalTitleFromRestoredSession_RejectsExplicitlyPollutedSummary(t *testing.T) {
	stub := titleRestoreAgentStub{
		summary: &agent.SessionSummary{
			OriginalFirstUserMessage:                    "[Context Summary]\n## Goal\nrefactor",
			OriginalFirstUserMessageIsCompactionSummary: true,
		},
		messages: []message.Message{
			{Role: "user", Content: "[Context Summary]\n## Goal\nrefactor", IsCompactionSummary: true},
			{Role: "assistant", Content: "ok"},
			{Role: "user", Content: "请修复登录闪退"},
		},
	}
	m := NewModelWithSize(stub, 80, 24)
	m.updateTerminalTitleFromRestoredSession()
	got := m.terminalTitleBase
	want := "请修复登录闪退"
	if got != want {
		t.Fatalf("terminalTitleBase = %q, want %q", got, want)
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
			{Role: "user", Content: "更近的请求"},
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
