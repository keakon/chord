package tui

import (
	"strings"
	"testing"

	"github.com/keakon/lipgloss/v2"
	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/markdownrender"
	"github.com/keakon/chord/internal/tui/syntax"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestConfirmDialogDoneMarkdownDoesNotLeakAssistantCardBackground(t *testing.T) {
	ApplyTheme(uitheme.Default())
	contentMarkdown = markdownrender.New(currentStyles.Palette)

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
	plain := terminaltext.StripANSI(out)
	if !strings.Contains(plain, "Title") || !strings.Contains(plain, "hello") {
		t.Fatalf("missing rendered done report text in dialog: %q", plain)
	}

	summary := buildConfirmSummary(req.ToolName, req.ArgsJSON, req.NeedsApproval, req.AlreadyAllowed, req.DoneReport)
	lines := m.renderConfirmSummary("title", summary, 118)
	assistantBgSeq := terminaltext.BackgroundSequence(currentStyles.Palette.AssistantCardBg)
	if assistantBgSeq == "" {
		t.Fatal("expected assistant card background ANSI sequence")
	}
	for _, line := range lines[2:] {
		if strings.Contains(line, assistantBgSeq) {
			t.Fatalf("Done confirmation markdown leaked assistant card background into dialog line: %q", line)
		}
	}

	globalCodeBgSeq := syntax.ColorSequence(lipgloss.Color(currentStyles.Palette.CodeBlockBg), false)
	if globalCodeBgSeq == "" {
		t.Fatal("expected global code block background ANSI sequence")
	}
	if strings.Contains(strings.Join(lines, "\n"), globalCodeBgSeq) {
		t.Fatal("Done confirmation markdown should not use assistant card code block background")
	}

	dialogCodeBgSeq := syntax.ColorSequence(lipgloss.Color(currentStyles.Palette.DialogCodeBlockBg), false)
	if dialogCodeBgSeq == "" {
		t.Fatal("expected dialog code block background ANSI sequence")
	}
	if !strings.Contains(strings.Join(lines, "\n"), dialogCodeBgSeq) {
		t.Fatal("expected fenced code block to use dialog code block background")
	}
}

func TestConfirmDialogDoneMarkdownUsesDialogCellBackgrounds(t *testing.T) {
	ApplyTheme(uitheme.Default())
	contentMarkdown = markdownrender.New(currentStyles.Palette)

	m := NewModelWithSize(nil, 120, 40)
	m.confirm.request = &ConfirmRequest{
		ToolName: "done",
		DoneReport: "Plain text before code.\n\n```bash\ngo test ./internal/tui -run\n" +
			"'TestQuestion|TestConfirm|TestHandleConfirm|TestResolveConfirm' -count=1\n```\n",
	}

	out := m.renderConfirmDialog()
	plainLine := findRenderedLineContaining(out, "Plain text before code.")
	if plainLine == "" {
		t.Fatalf("missing plain markdown line in dialog: %q", terminaltext.StripANSI(out))
	}
	codeLine := findRenderedLineContaining(out, "go test ./internal/tui -run")
	if codeLine == "" {
		t.Fatalf("missing code line in dialog: %q", terminaltext.StripANSI(out))
	}

	dialogBg := colorOfTheme(currentStyles.Palette.DialogBg)
	plainCell, ok := findRenderedCell(plainLine, "P")
	if !ok {
		t.Fatalf("missing plain text cell in line: %q", terminaltext.StripANSI(plainLine))
	}
	if !colorsEqual(plainCell.Style.Bg, dialogBg) {
		t.Fatalf("plain markdown cell background = %v, want dialog bg %v", plainCell.Style.Bg, dialogBg)
	}

	dialogCodeBg := colorOfTheme(currentStyles.Palette.DialogCodeBlockBg)
	globalCodeBg := colorOfTheme(currentStyles.Palette.CodeBlockBg)
	if colorsEqual(dialogCodeBg, globalCodeBg) {
		t.Fatal("test requires dialog code block bg to differ from assistant code block bg")
	}
	codeCell, ok := findRenderedCell(codeLine, "g")
	if !ok {
		t.Fatalf("missing code text cell in line: %q", terminaltext.StripANSI(codeLine))
	}
	if !colorsEqual(codeCell.Style.Bg, dialogCodeBg) {
		t.Fatalf("dialog code cell background = %v, want dialog code bg %v", codeCell.Style.Bg, dialogCodeBg)
	}
	if colorsEqual(codeCell.Style.Bg, globalCodeBg) {
		t.Fatalf("dialog code cell used assistant code block bg %v", globalCodeBg)
	}
}

func findRenderedLineContaining(out, needle string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(terminaltext.StripANSI(line), needle) {
			return line
		}
	}
	return ""
}

func findRenderedCell(line, content string) (uv.Cell, bool) {
	width := ansi.StringWidth(line)
	if width <= 0 {
		width = 1
	}
	buf := newScreenBuffer(width, 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	for _, cell := range buf.Line(0) {
		if cell.Content == content {
			return cell, true
		}
	}
	return uv.Cell{}, false
}
