package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
)

func TestLooksLikeCompactionSummaryPreview(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", true},
		{"plain user message", "fix login bug", false},
		{"raw header", "[Context Summary]\n## Goal\nrefactor", true},
		{"header without trailing newline", "[Context Summary] start of summary", true},
		{"leading whitespace then header", "  \t[Context Summary]\n## Goal", true},
		{"only goal heading without prefix", "## Goal\nrefactor", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeCompactionSummaryPreview(c.in); got != c.want {
				t.Fatalf("looksLikeCompactionSummaryPreview(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

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

func TestUpdateTerminalTitleFromRestoredSession_RejectsPollutedSummary(t *testing.T) {
	stub := titleRestoreAgentStub{
		summary: &agent.SessionSummary{
			OriginalFirstUserMessage: "[Context Summary]\n## Goal\nrefactor",
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
