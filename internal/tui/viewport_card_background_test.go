package tui

import (
	"strings"
	"testing"

	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// TestViewportKeepsToolCardBackgroundEdge verifies the viewport render path
// leaves the tool-card background exactly where the raw card renderer put it:
// the surface fills the full viewport width (after the rail column), and the
// viewport's per-line padding must never extend, shrink or move that
// background. Rows whose background fills the surface must reach the same
// right edge as in the raw render.
func TestViewportKeepsToolCardBackgroundEdge(t *testing.T) {
	ApplyTheme(DefaultTheme())
	const width = 180
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       `{"patch":"*** Begin Patch\n*** End Patch"}`,
		ResultContent: "short diagnostic",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
	}

	raw := block.Render(width, "")
	rawScreen := newScreenBuffer(width, len(raw))
	uv.NewStyledString(strings.Join(raw, "\n")).Draw(rawScreen, rawScreen.Bounds())

	viewport := NewViewport(width, len(raw))
	viewport.AppendBlock(block)
	rendered := strings.Split(viewport.Render("", nil, -1, 0, ""), "\n")
	screen := newScreenBuffer(width, len(rendered))
	uv.NewStyledString(strings.Join(rendered, "\n")).Draw(screen, screen.Bounds())

	cardBg := colorOfTheme(currentTheme.ToolCallBg)
	checked := 0
	maxEdge := -1
	for y := range raw {
		wantEdge := -1
		for x := range width {
			if cell := rawScreen.Line(y).At(x); cell != nil && colorsEqual(cell.Style.Bg, cardBg) {
				wantEdge = x
			}
		}
		if wantEdge < 0 {
			continue
		}
		if wantEdge > maxEdge {
			maxEdge = wantEdge
		}
		checked++
		gotEdge := -1
		for x := range width {
			if cell := screen.Line(y).At(x); cell != nil && colorsEqual(cell.Style.Bg, cardBg) {
				gotEdge = x
			}
		}
		if gotEdge != wantEdge {
			t.Fatalf("row %d: viewport moved card background edge from %d to %d", y, wantEdge, gotEdge)
		}
		for x := wantEdge + 1; x < width; x++ {
			if cell := screen.Line(y).At(x); cell != nil && !cell.Style.Bg.IsZero() {
				t.Fatalf("row %d: column %d outside card surface still has background %v", y, x, cell.Style.Bg)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tool-card rows with the expected background were rendered")
	}
	// The card surface fills the viewport: its right edge must reach the last
	// column (no stray margin or early cap leaves a gap before the sidebar).
	if maxEdge < width-1 {
		t.Fatalf("card surface ends at column %d, want it to fill the viewport width %d", maxEdge, width)
	}
}

// TestViewportDoesNotLetExpandedTabsOrCarriageReturnsCorruptToolCard verifies
// that display-only tab expansion and control-character sanitization cannot
// leave a right-edge gap or overwrite the card rail.
func TestViewportDoesNotLetExpandedTabsOrCarriageReturnsCorruptToolCard(t *testing.T) {
	ApplyTheme(DefaultTheme())
	const width = 290
	patchArgs := `{"patch":"*** Begin Patch\n*** Update File: internal/tui/tool_card_width_test.go\n@@\n \t\r\n*** End Patch"}`
	block := &Block{
		ID:            2,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(patchArgs),
		RawArgs:       patchArgs,
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "apply_patch failed: no changes were committed.\n\nNot applied:\n- internal/tui/tool_card_width_test.go: hunk not found (1/1); first expected complete line: `\t\tif isFoo(err) {`; the file currently has a different guard",
	}

	raw := block.Render(width, "")
	for i, line := range raw {
		if strings.ContainsRune(line, '\r') {
			t.Fatalf("raw card line %d still contains carriage return: %q", i, line)
		}
	}
	rawScreen := newScreenBuffer(width, len(raw))
	uv.NewStyledString(strings.Join(raw, "\n")).Draw(rawScreen, rawScreen.Bounds())

	viewport := NewViewport(width, len(raw))
	viewport.AppendBlock(block)
	rendered := strings.Split(viewport.Render("", nil, -1, 0, ""), "\n")
	screen := newScreenBuffer(width, len(rendered))
	uv.NewStyledString(strings.Join(rendered, "\n")).Draw(screen, screen.Bounds())

	cardBg := colorOfTheme(currentTheme.ToolCallBg)
	filled := 0
	for y := range raw {
		rawEdge := -1
		for x := range width {
			if cell := rawScreen.Line(y).At(x); cell != nil && colorsEqual(cell.Style.Bg, cardBg) {
				rawEdge = x
			}
		}
		if rawEdge < 0 {
			continue
		}
		filled++
		if rawEdge != width-1 {
			t.Fatalf("raw row %d: card background ends at column %d, want it to fill the viewport width %d", y, rawEdge, width)
		}
		gotEdge := -1
		for x := range width {
			if cell := screen.Line(y).At(x); cell != nil && colorsEqual(cell.Style.Bg, cardBg) {
				gotEdge = x
			}
		}
		if gotEdge != rawEdge {
			t.Fatalf("viewport row %d: card background edge moved from %d to %d", y, rawEdge, gotEdge)
		}
	}
	if filled == 0 {
		t.Fatal("no tool-card background was rendered")
	}

	railRows := 0
	railFg := colorOfTheme(currentTheme.RailToolFg)
	for y, line := range raw {
		if !strings.HasPrefix(stripANSI(line), "│") {
			continue
		}
		railRows++
		cell := rawScreen.Line(y).At(0)
		if cell == nil || cell.Content != "│" || !colorsEqual(cell.Style.Fg, railFg) {
			t.Fatalf("raw row %d: rail at column 0 was overwritten: cell=%+v", y, cell)
		}
	}
	if railRows == 0 {
		t.Fatal("no rail-bearing card rows were rendered")
	}
}
