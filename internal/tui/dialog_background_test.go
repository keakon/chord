package tui

import (
	"image"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/agent"
)

func dialogBgColor(t *testing.T) interface {
	RGBA() (r, g, b, a uint32)
} {
	t.Helper()
	ApplyTheme(DefaultTheme())
	if currentTheme.DialogBg == "" {
		t.Skip("theme has no dialog background")
	}
	return colorOfTheme(currentTheme.DialogBg)
}

func drawLineCells(t *testing.T, line string) uv.Line {
	t.Helper()
	width := ansi.StringWidth(line)
	if width <= 0 {
		t.Fatalf("line has no visible width: %q", stripANSI(line))
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
	plain := stripANSI(line)
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
		ConfirmAllowStyle.Render("[y] Delete"),
		DimStyle.Render("  "),
		ConfirmDenyStyle.Render("[n/esc] Cancel"),
	)
	out := renderDialogBox(60, []string{line})
	buttonLine := findRenderedLineContaining(out, "[n/esc] Cancel")
	if buttonLine == "" {
		t.Fatalf("missing cancel segment after preserve: %q", stripANSI(out))
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
		t.Fatalf("missing button line: %q", stripANSI(out))
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
		t.Fatalf("missing scope line: %q", stripANSI(out))
	}
	requireCellBg(t, scopeLine, "Scope:", "S", wantBg)
	requireCellBg(t, scopeLine, "Scope: session", "s", wantBg)
	toolLine := findRenderedLineContaining(out, "shell")
	if toolLine == "" {
		t.Fatalf("missing tool input value: %q", stripANSI(out))
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
		t.Fatalf("missing deny input value: %q", stripANSI(out))
	}
	requireCellBg(t, line, "use reviewer first", "u", wantBg)
}

func TestRenderOverlayPreservesButtons(t *testing.T) {
	wantBg := dialogBgColor(t)
	m := NewModelWithSize(nil, 120, 32)
	content := lipgloss.JoinHorizontal(lipgloss.Left,
		ConfirmAllowStyle.Render("[Enter/A] Allow"),
		DimStyle.Render("  "),
		ConfirmDenyStyle.Render("[Esc/D] Deny"),
	)
	dialog, _ := RenderOverlay(OverlayConfig{
		Title:    "Buttons",
		Hint:     "esc close",
		MinWidth: 40,
		MaxWidth: 70,
	}, content, 1, image.Rect(0, 0, m.width, m.height))
	line := findRenderedLineContaining(dialog, "[Esc/D] Deny")
	if line == "" {
		t.Fatalf("missing deny button: %q", stripANSI(dialog))
	}
	requireCellBg(t, line, "[Esc/D] Deny", "D", wantBg)
}
