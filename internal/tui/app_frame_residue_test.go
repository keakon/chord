package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	uv "github.com/keakon/ultraviolet"
)

// The host keeps whatever the incremental renderer stops rewriting, so these
// tests pin the two frame invariants that keep stale glyphs off the screen: the
// last physical column stays an EmptyCell, and dismissing a bottom-left overlay
// asks for a full repaint.
//
// The terminal's last physical column must stay an EmptyCell: writing it (even
// with a blank space) makes some hosts emit an extra wrap for the frame, and the
// diff renderer only rewrites cells it believes changed, so the resulting stale
// row would survive until a full repaint.
func assertLastColumnEmpty(t *testing.T, m *Model) {
	t.Helper()
	for y := range m.height {
		line := m.screenBuf.Line(y)
		if len(line) < m.width {
			t.Fatalf("row %d has %d cells, want at least %d", y, len(line), m.width)
		}
		if cell := &line[m.width-1]; !cell.Equal(&uv.EmptyCell) {
			t.Fatalf("cell[%d,%d] = %#v, want EmptyCell", m.width-1, y, cell)
		}
	}
}

func TestStatusBarAndSeparatorLeaveLastColumnEmpty(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 107, 61)

	// A status line that consumes the whole content budget must still stop one
	// cell short of the terminal edge.
	if got := ansi.StringWidth(m.renderStatusBarLine(strings.Repeat("x", m.width))); got != m.width-1 {
		t.Fatalf("status bar width = %d, want %d", got, m.width-1)
	}
	if got := ansi.StringWidth(m.renderAnimatedInputSeparator(m.drawableLineWidth())); got != m.width-1 {
		t.Fatalf("input separator width = %d, want %d", got, m.width-1)
	}
}

func TestViewKeepsLastColumnEmptyWithSlashCompletion(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 107, 61)
	m.mode = ModeInsert
	m.input.SetValue("/resume")

	if drop := m.renderSlashCompletionDropdown(m.input.DisplayValue()); drop == "" {
		t.Fatal("expected slash completion dropdown for /resume")
	}
	m.View()
	assertLastColumnEmpty(t, &m)
}

func TestViewKeepsLastColumnEmptyOnNarrowEmptySessionSelect(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 107, 61)
	m.mode = ModeInsert

	m.View()
	assertLastColumnEmpty(t, &m)

	// Narrow (no info panel) plus an empty prefetched session list is the state
	// that used to leave a stale status row on screen.
	m.openSessionSelect(nil, true)
	m.View()
	assertLastColumnEmpty(t, &m)

	m.handleSessionSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m.View()
	assertLastColumnEmpty(t, &m)
}

func TestViewKeepsLastColumnEmptyOnNarrowWelcome(t *testing.T) {
	ApplyTheme(DefaultTheme())
	// The welcome hints are wider than a narrow terminal, and the splash art can
	// be too. The frame must still stop one column short of the edge instead of
	// truncating onto it, at every size.
	for _, size := range [][2]int{{107, 61}, {60, 20}, {40, 12}, {30, 8}} {
		m := NewModelWithSize(nil, size[0], size[1])
		m.mode = ModeInsert
		m.View()
		assertLastColumnEmpty(t, &m)
	}
}

// frameText flattens the rendered screen buffer so a test can assert on what the
// frame actually shows rather than on the inputs that produced it.
func frameText(m *Model) string {
	var sb strings.Builder
	for y := range m.height {
		for _, cell := range m.screenBuf.Line(y) {
			sb.WriteString(cell.Content)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// requestsFullRepaint reports whether running cmd asks Bubble Tea for a
// clear-screen, the only thing that wipes cells the diff renderer stopped
// rewriting.
func requestsFullRepaint(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, sub := range msg {
			if requestsFullRepaint(t, sub) {
				return true
			}
		}
		return false
	default:
		return fmt.Sprintf("%T", msg) == "tea.clearScreenMsg"
	}
}

func TestDismissingBottomLeftOverlayRequestsFullRepaint(t *testing.T) {
	ApplyTheme(DefaultTheme())
	esc := tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	newInsertModel := func() *Model {
		m := NewModelWithSize(nil, 107, 61)
		m.mode = ModeInsert
		return &m
	}
	// Update repays the repaint the draw loop owes for the overlay it drew, so
	// each case renders once with the overlay in place before dismissing it.

	t.Run("slash completion dropdown", func(t *testing.T) {
		m := newInsertModel()
		m.input.SetValue("/resume")
		if !m.bottomLeftOverlayDrawn() {
			t.Fatal("slash completion dropdown should be drawn for /resume")
		}
		m.View()

		_, cmd := m.Update(esc)
		if m.bottomLeftOverlayDrawn() {
			t.Fatal("escaping insert mode should drop the dropdown")
		}
		if !requestsFullRepaint(t, cmd) {
			t.Fatal("dismissing the dropdown must request a full repaint")
		}
	})

	t.Run("@ mention list", func(t *testing.T) {
		m := newInsertModel()
		m.atMentionOpen = true
		m.atMentionList = NewOverlayList([]OverlayListItem{{Label: "main.go"}}, 10)
		if !m.bottomLeftOverlayDrawn() {
			t.Fatal("@ mention list should count as drawn while it is open")
		}
		m.View()

		_, cmd := m.Update(esc)
		if m.bottomLeftOverlayDrawn() {
			t.Fatal("escape should close the @ mention list")
		}
		if !requestsFullRepaint(t, cmd) {
			t.Fatal("closing the @ mention list must request a full repaint")
		}
	})

	t.Run("kitty placements are forgotten", func(t *testing.T) {
		m := newInsertModel()
		m.input.SetValue("/resume")
		m.kittyPlacementCache[123] = struct{}{}
		m.View()

		m.Update(esc)
		if _, ok := m.kittyPlacementCache[123]; ok {
			t.Fatal("a full repaint must forget kitty placements so they are re-announced")
		}
	})

	t.Run("typing without an overlay", func(t *testing.T) {
		m := newInsertModel()
		m.input.SetValue("hello")

		_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
		if requestsFullRepaint(t, cmd) {
			t.Fatal("typing without an overlay must keep incremental rendering")
		}
	})
}

func TestBottomLeftOverlayNotReportedWhenDropdownOutgrowsMainArea(t *testing.T) {
	ApplyTheme(DefaultTheme())
	// A terminal too short for the dropdown: the renderer skips it entirely, so
	// the predicate must not claim it covers the bottom-left cell. Reporting it
	// would force a full clear-screen on dismissal for a layer that was never
	// drawn.
	m := NewModelWithSize(nil, 60, 10)
	m.mode = ModeInsert
	m.input.SetValue("/")
	m.layout = m.generateLayout(m.width, m.height)

	drop, dropLines := m.slashCompletionOverlay()
	if drop == "" {
		t.Fatal("expected a slash completion dropdown for /")
	}
	if dropLines <= m.layout.main.Dy() {
		t.Fatalf("test needs a dropdown taller than the main area: %d rows vs %d", dropLines, m.layout.main.Dy())
	}
	if m.bottomLeftOverlayDrawn() {
		t.Fatalf("predicate reports a dropdown the renderer will not draw: %d rows > main area %d",
			dropLines, m.layout.main.Dy())
	}

	// The renderer agrees: the dropdown's own help line never reaches the frame.
	m.View()
	if strings.Contains(frameText(&m), "Tab/Enter complete") {
		t.Fatalf("renderer drew the oversized dropdown anyway:\n%s", frameText(&m))
	}

	// Dismissing it must therefore stay on the incremental path.
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if requestsFullRepaint(t, cmd) {
		t.Fatal("a dropdown that was never drawn must not force a full repaint")
	}
}
