package tui

import (
	"strings"
	"testing"
)

func TestConfirmDialogDoneMarkdownPreservesBackgroundAcrossANSIGaps(t *testing.T) {
	ApplyTheme(DefaultTheme())
	resetMarkdownRenderer()

	m := NewModelWithSize(nil, 120, 40)
	req := &ConfirmRequest{
		ToolName:   "Done",
		DoneReport: "**标题**：`list[str]` → JSON\n\n- item1\n- item2",
	}
	m.confirm.request = req

	out := m.renderConfirmDialog()
	if out == "" {
		t.Fatal("expected non-empty confirm dialog")
	}
	plain := stripANSI(out)
	if !strings.Contains(plain, "标题") {
		t.Fatalf("missing rendered done report text in dialog: %q", plain)
	}

	bgSeq := colorToANSIBgSeq(currentTheme.AssistantCardBg)
	if bgSeq == "" {
		t.Fatal("expected assistant card background ANSI sequence")
	}
	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed, req.DoneReport)
	lines := m.renderConfirmSummary("title", summary, 118)
	for _, line := range lines[2:] {
		plainLine := stripANSI(line)
		if !strings.Contains(plainLine, "标题") && !strings.Contains(plainLine, "item") {
			continue
		}
		for _, reset := range []string{"\x1b[m", "\x1b[0m", "\x1b[39m", "\x1b[49m"} {
			remaining := line
			for {
				idx := strings.Index(remaining, reset)
				if idx < 0 {
					break
				}
				after := remaining[idx+len(reset):]
				if strings.HasPrefix(after, " ") {
					t.Fatalf("found %q followed by padding space without background re-apply in %q", reset, line)
				}
				if strings.HasPrefix(after, bgSeq) {
					remaining = after[len(bgSeq):]
				} else {
					remaining = after
				}
			}
		}
	}
}
