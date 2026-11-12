package tui

import (
	"image"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"
	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/permission/rulesuggest"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func dialogBgColor(t *testing.T) interface {
	RGBA() (r, g, b, a uint32)
} {
	t.Helper()
	ApplyTheme(uitheme.Default())
	if currentStyles.Palette.DialogBg == "" {
		t.Skip("theme has no dialog background")
	}
	return colorOfTheme(currentStyles.Palette.DialogBg)
}

func drawLineCells(t *testing.T, line string) uv.Line {
	t.Helper()
	width := ansi.StringWidth(line)
	if width <= 0 {
		t.Fatalf("line has no visible width: %q", terminaltext.StripANSI(line))
	}
	buf := newScreenBuffer(width, 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	return buf.Line(0)
}

func requireCellBg(t *testing.T, line, needle, wantChar string, wantBg interface {
	RGBA() (r, g, b, a uint32)
}) {
	t.Helper()
	cells := drawLineCells(t, line)
	plain := terminaltext.StripANSI(line)
	idx := strings.Index(plain, needle)
	if idx < 0 {
		t.Fatalf("line %q does not contain %q", plain, needle)
	}
	// Tests use ASCII needles so byte offset == column offset.
	prefix := plain[:idx] + needle[:strings.Index(needle, wantChar)]
	col := ansi.StringWidth(prefix)
	if col < 0 || col >= len(cells) {
		t.Fatalf("column %d out of range (cells=%d) for %q in %q", col, len(cells), needle, plain)
	}
	cell := cells[col]
	if string(cell.Content) != wantChar {
		t.Fatalf("cell content = %q, want %q (line %q)", cell.Content, wantChar, plain)
	}
	if !colorsEqual(cell.Style.Bg, wantBg) {
		t.Fatalf("cell %q background = %v, want dialog bg %v (line %q)", wantChar, cell.Style.Bg, wantBg, plain)
	}
}

func TestPreserveDialogBackgroundKeepsMultiSegmentLine(t *testing.T) {
	wantBg := dialogBgColor(t)
	line := lipgloss.JoinHorizontal(lipgloss.Left,
		renderHintChip(dangerHint("y", "Delete")),
		currentStyles.DimStyle.Render("  "),
		renderHintChip(hint("n/esc", "Cancel")),
	)
	out := renderDialogBox(60, []string{line})
	buttonLine := findRenderedLineContaining(out, "[n/esc] Cancel")
	if buttonLine == "" {
		t.Fatalf("missing cancel segment after preserve: %q", terminaltext.StripANSI(out))
	}
	// Content cells (including the spacer) must sit on DialogBg; border cells
	// carry the border style and are intentionally excluded.
	requireCellBg(t, buttonLine, "[y] Delete", "D", wantBg)
	requireCellBg(t, buttonLine, "[n/esc] Cancel", "C", wantBg)
}

func TestDeleteSessionCancelKeepsDialogBg(t *testing.T) {
	wantBg := dialogBgColor(t)
	m := NewModelWithSize(nil, 120, 32)
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session: &agent.SessionSummary{
			ID:               "20260917015034212",
			FirstUserMessage: "sample first message",
		},
		prevMode: ModeSessionSelect,
	}
	out := m.renderSessionDeleteConfirmDialog()
	if out == "" {
		t.Fatal("expected non-empty delete dialog")
	}
	line := findRenderedLineContaining(out, "[n/esc] Cancel")
	if line == "" {
		t.Fatalf("missing button line: %q", terminaltext.StripANSI(out))
	}
	requireCellBg(t, line, "[y] Delete", "D", wantBg)
	requireCellBg(t, line, "[n/esc] Cancel", "C", wantBg)
}

func TestRulesAddKeepsDialogBg(t *testing.T) {
	wantBg := dialogBgColor(t)
	m := NewModelWithSize(nil, 120, 32)
	_ = m.openRules()
	_ = m.startAddRule()
	m.rules.addToolInput.SetValue("shell")
	m.rules.addPatInput.SetValue("git *")
	out := m.renderRulesAdd(80)
	if out == "" {
		t.Fatal("expected non-empty rules add dialog")
	}
	scopeLine := findRenderedLineContaining(out, "Scope:")
	if scopeLine == "" {
		t.Fatalf("missing scope line: %q", terminaltext.StripANSI(out))
	}
	requireCellBg(t, scopeLine, "Scope:", "S", wantBg)
	requireCellBg(t, scopeLine, "Scope: session", "s", wantBg)
	toolLine := findRenderedLineContaining(out, "shell")
	if toolLine == "" {
		t.Fatalf("missing tool input value: %q", terminaltext.StripANSI(out))
	}
	requireCellBg(t, toolLine, "shell", "s", wantBg)
}

func TestHandoffDenyReasonKeepsDialogBg(t *testing.T) {
	wantBg := dialogBgColor(t)
	backend := &sessionControlAgent{availableAgents: []string{"builder", "reviewer"}}
	m := NewModelWithSize(backend, 120, 32)
	m.openHandoffSelect("docs/plans/example.md", "req-1", "main", m.mode)
	_ = m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
	if !m.handoffSelect.denyingWithReason {
		t.Fatal("expected handoff deny reason mode")
	}
	m.handoffSelect.denyReasonInput.SetValue("use reviewer first")
	out := m.renderHandoffSelectDialog()
	if out == "" {
		t.Fatal("expected non-empty handoff deny dialog")
	}
	line := findRenderedLineContaining(out, "use reviewer first")
	if line == "" {
		t.Fatalf("missing deny input value: %q", terminaltext.StripANSI(out))
	}
	requireCellBg(t, line, "use reviewer first", "u", wantBg)
}

// requireTextCellsStyle checks every cell of needle in line, so a surface that
// breaks partway through the text is caught, not only its first character.
func requireTextCellsStyle(t *testing.T, line, needle string, wantBg, wantFg interface {
	RGBA() (r, g, b, a uint32)
}) {
	t.Helper()
	cells := drawLineCells(t, line)
	plain := terminaltext.StripANSI(line)
	idx := strings.Index(plain, needle)
	if idx < 0 {
		t.Fatalf("line %q does not contain %q", plain, needle)
	}
	start := ansi.StringWidth(plain[:idx])
	end := start + ansi.StringWidth(needle)
	if end > len(cells) {
		t.Fatalf("needle %q ends at column %d past %d cells in %q", needle, end, len(cells), plain)
	}
	for col := start; col < end; col++ {
		cell := cells[col]
		if !colorsEqual(cell.Style.Bg, wantBg) {
			t.Fatalf("cell %d %q of %q background = %v, want %v (line %q)", col, cell.Content, needle, cell.Style.Bg, wantBg, plain)
		}
		if wantFg != nil && !colorsEqual(cell.Style.Fg, wantFg) {
			t.Fatalf("cell %d %q of %q foreground = %v, want %v (line %q)", col, cell.Content, needle, cell.Style.Fg, wantFg, plain)
		}
	}
}

// The selected rule-picker row embeds a separately styled tool label whose SGR
// reset used to end the selection surface: the pattern and summary after the
// label fell back to DialogBg while only the trailing padding stayed selected.
func TestRulePickerSelectedRowKeepsSelectionSurfaceAfterToolLabel(t *testing.T) {
	dialogBg := dialogBgColor(t)
	selectedBg := colorOfTheme(currentStyles.Palette.SelectedBg)
	selectedFg := colorOfTheme(currentStyles.Palette.SelectedFg)
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{ToolName: tools.NameDelete, ArgsJSON: `{}`}
	m.confirm.pickingRule = true
	m.confirm.candidates = []rulesuggest.Candidate{
		{Tool: "delete", Pattern: "/tmp/sample.txt", Summary: "one file"},
		{Tool: "delete", Pattern: "/tmp/other.txt"},
	}
	m.confirm.patternIdx = 0
	m.confirm.selectedPatterns = map[int]struct{}{0: {}}

	out := m.renderConfirmDialog()
	selected := findRenderedLineContaining(out, "/tmp/sample.txt")
	if selected == "" {
		t.Fatalf("missing selected candidate row: %q", terminaltext.StripANSI(out))
	}
	requireTextCellsStyle(t, selected, "❯ [x] ", selectedBg, selectedFg)
	requireTextCellsStyle(t, selected, "delete", selectedBg, nil)
	requireTextCellsStyle(t, selected, " /tmp/sample.txt — one file   ", selectedBg, nil)
	requireTextCellsStyle(t, selected, "/tmp/sample.txt — one file", selectedBg, selectedFg)

	unselected := findRenderedLineContaining(out, "/tmp/other.txt")
	if unselected == "" {
		t.Fatalf("missing unselected candidate row: %q", terminaltext.StripANSI(out))
	}
	requireTextCellsStyle(t, unselected, "[ ] delete /tmp/other.txt   ", dialogBg, nil)
}

func TestRenderOverlayPreservesButtons(t *testing.T) {
	wantBg := dialogBgColor(t)
	m := NewModelWithSize(nil, 120, 32)
	content := lipgloss.JoinHorizontal(lipgloss.Left,
		renderHintChip(hint("Enter/A", "Allow")),
		currentStyles.DimStyle.Render("  "),
		renderHintChip(hint("Esc/D", "Deny")),
	)
	dialog, _ := RenderOverlay(OverlayConfig{
		Title: "Buttons",
		Hint:  hintLine(hint("Esc", "close")),

		MaxWidth: 70,
	}, content, image.Rect(0, 0, m.width, m.height))
	line := findRenderedLineContaining(dialog, "[Esc/D] Deny")
	if line == "" {
		t.Fatalf("missing deny button: %q", terminaltext.StripANSI(dialog))
	}
	requireCellBg(t, line, "[Esc/D] Deny", "D", wantBg)
}
