package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestConfirmDialogDoneMarkdownDoesNotLeakAssistantCardBackground(t *testing.T) {
	ApplyTheme(DefaultTheme())
	resetMarkdownRenderer()

	m := NewModelWithSize(nil, 120, 40)
	req := &ConfirmRequest{
		ToolName:   "done",
		DoneReport: "**Title**: `list[str]` → JSON\n\n```text\nhello\n```\n",
	}
	m.confirm.request = req

	out := m.renderConfirmDialog()
	if out == "" {
		t.Fatal("expected non-empty confirm dialog")
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "Title") || !strings.Contains(plain, "hello") {
		t.Fatalf("missing rendered done report text in dialog: %q", plain)
	}

	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed, req.DoneReport)
	lines := m.renderConfirmSummary("title", summary, 118)
	assistantBgSeq := colorToANSIBgSeq(currentTheme.AssistantCardBg)
	if assistantBgSeq == "" {
		t.Fatal("expected assistant card background ANSI sequence")
	}
	for _, line := range lines[2:] {
		if strings.Contains(line, assistantBgSeq) {
			t.Fatalf("Done confirmation markdown leaked assistant card background into dialog line: %q", line)
		}
	}

	codeBgSeq := ansiSeqForColor(lipgloss.Color(currentTheme.CodeBlockBg), false)
	if codeBgSeq == "" {
		t.Fatal("expected code block background ANSI sequence")
	}
	if !strings.Contains(strings.Join(lines, "\n"), codeBgSeq) {
		t.Fatal("expected fenced code block to retain code block background")
	}
}
