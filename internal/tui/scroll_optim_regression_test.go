package tui

// These tests pin down the byte-level behavior of Ultraviolet's renderer in
// the two scroll configurations that matter for the Ghostty DECSTBM
// carve-out. They model a chord-shaped frame (sticky transcript above a
// fixed separator/input/status block) and assert that:
//
//   - With scroll optimization on (the upstream default), streaming a one-line
//     scroll emits a DECSTBM region scroll. This is the baseline behavior that
//     leaves stale rows behind on libghostty during post-focus-restore.
//
//   - With scroll-region optimization off (the WithoutScrollRegionOptimization
//     path used by Chord's TUI), the same scroll can keep safe hard-scroll
//     optimizations while emitting no DECSTBM region setup.
//
// If a future Bubble Tea / Ultraviolet upgrade silently changes these
// guarantees, the Ghostty separator-artifact fix may regress, and these tests
// surface that regression directly.

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	uv "github.com/keakon/ultraviolet"
)

// padToWidth pads s with spaces so its display width equals width. Inputs
// wider than width are left untouched (callers control that).
func padToWidth(s string, width int) string {
	w := ansi.StringWidth(s)
	if w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// buildScrollFrame composes a single full-screen frame mimicking chord's
// layout: a transcript area whose lines depend on `base`, a fixed horizontal
// separator row, an input prompt row, and a status row at the bottom. The
// separator and input/status rows are identical across frames; only the
// transcript scrolls.
func buildScrollFrame(width, height, base int) string {
	sepRow := height - 4
	inputRow := height - 3
	statusRow := height - 1
	var b strings.Builder
	for y := 0; y < height; y++ {
		var line string
		switch y {
		case sepRow:
			line = strings.Repeat("─", width)
		case inputRow:
			line = "> "
		case statusRow:
			line = " STATUS BAR"
		default:
			if y < sepRow {
				line = fmt.Sprintf("│ transcript line %d", base+y)
			}
		}
		b.WriteString(padToWidth(line, width))
		if y < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderFrame mirrors the cursed renderer's flush path: clear the cell buffer,
// draw the styled content into it, ask the terminal renderer to diff against
// its current view, and flush the resulting bytes.
func renderFrame(t *testing.T, scr *uv.TerminalRenderer, cell *uv.ScreenBuffer, out *bytes.Buffer, content string) string {
	t.Helper()
	out.Reset()
	cell.Clear()
	uv.NewStyledString(content).Draw(cell, cell.Bounds())
	scr.Render(cell.RenderBuffer)
	if err := scr.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return out.String()
}

// newScrollScenario returns the renderer/buffer pair plus the initial frame's
// output (which is always a full repaint and therefore not interesting for the
// scroll-bytes assertions).
func newScrollScenario(t *testing.T, width, height int, scrollOptim bool) (*uv.TerminalRenderer, *uv.ScreenBuffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	scr := uv.NewTerminalRenderer(out, nil)
	scr.SetFullscreen(true)
	scr.SetRelativeCursor(false)
	scr.SetScrollOptim(scrollOptim)
	cell := uv.NewScreenBuffer(width, height)
	_ = renderFrame(t, scr, &cell, out, buildScrollFrame(width, height, 0))
	return scr, &cell, out
}

func newScrollRegionScenario(t *testing.T, width, height int, scrollOptim, scrollRegionOptim bool) (*uv.TerminalRenderer, *uv.ScreenBuffer, *bytes.Buffer) {
	scr, cell, out := newScrollScenario(t, width, height, scrollOptim)
	scr.SetScrollRegionOptim(scrollRegionOptim)
	return scr, cell, out
}

// decstbmRE matches `ESC [ <top> ; <bottom> r`, i.e. ansi.SetTopBottomMargins.
var decstbmRE = regexp.MustCompile(`\x1b\[\d+;\d+r`)

func TestUVStreamingScrollUsesDECSTBM(t *testing.T) {
	const width, height = 60, 16
	scr, cell, out := newScrollScenario(t, width, height, true)
	got := renderFrame(t, scr, cell, out, buildScrollFrame(width, height, 1))

	if !decstbmRE.MatchString(got) {
		t.Fatalf("expected DECSTBM region scroll with scrollOptim=true; got %q", got)
	}
	if strings.Count(got, "─") != 0 {
		t.Fatalf("baseline streaming scroll re-emitted separator runes; got %q", got)
	}
}

func TestUVStreamingScrollWithoutOptimAvoidsDECSTBM(t *testing.T) {
	const width, height = 60, 16
	scr, cell, out := newScrollScenario(t, width, height, false)
	got := renderFrame(t, scr, cell, out, buildScrollFrame(width, height, 1))

	if decstbmRE.MatchString(got) {
		t.Fatalf("fully disabled scroll optimization path must not emit DECSTBM; got %q", got)
	}
	if strings.Count(got, "─") != 0 {
		t.Fatalf("in-place scroll re-emitted separator runes; got %q", got)
	}
}

func TestUVStreamingScrollWithoutRegionOptimAvoidsDECSTBM(t *testing.T) {
	const width, height = 60, 16
	scr, cell, out := newScrollRegionScenario(t, width, height, true, false)
	got := renderFrame(t, scr, cell, out, buildScrollFrame(width, height, 1))

	if decstbmRE.MatchString(got) {
		t.Fatalf("WithoutScrollRegionOptimization path must not emit DECSTBM; got %q", got)
	}
	if strings.Count(got, "─") != 0 {
		t.Fatalf("scroll-region-disabled path re-emitted separator runes; got %q", got)
	}
}
