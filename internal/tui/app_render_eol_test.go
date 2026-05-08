package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestViewPropagatesWindowTitle(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 60, 12)
	m.terminalTitleView = "MyTitle"

	view := m.View()
	if view.WindowTitle != "MyTitle" {
		t.Fatalf("View.WindowTitle = %q, want %q", view.WindowTitle, "MyTitle")
	}
}

func TestViewPropagatesWindowTitleForCachedViews(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 60, 12)
	m.terminalTitleView = "CachedTitle"

	// Frozen path.
	m.renderFreezeActive = true
	m.cachedFrozenView = tea.View{Content: "frozen"}
	m.cachedFrozenViewValid = true
	frozen := m.View()
	if frozen.WindowTitle != "CachedTitle" {
		t.Fatalf("frozen View.WindowTitle = %q, want %q", frozen.WindowTitle, "CachedTitle")
	}

	// Deferred path.
	m.renderFreezeActive = false
	m.streamRenderDeferred = true
	m.streamRenderForceView = false
	m.displayState = stateForeground
	m.mode = ModeNormal
	m.cachedFullView = tea.View{Content: "cached"}
	m.cachedFullViewValid = true
	deferred := m.View()
	if deferred.Content != "cached" {
		t.Fatalf("deferred View.Content = %q, want cached", deferred.Content)
	}
	if deferred.WindowTitle != "CachedTitle" {
		t.Fatalf("deferred View.WindowTitle = %q, want %q", deferred.WindowTitle, "CachedTitle")
	}
}

func TestRenderScreenBufferFullFramePadsToWidthAndHeight(t *testing.T) {
	scr := newScreenBuffer(5, 3)
	uv.NewStyledString("abc\n\n").Draw(scr, scr.Bounds())

	out := renderScreenBufferFullFrame(scr, 5, 3)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != 5 {
			t.Fatalf("line %d width = %d, want 5; raw=%q", i, w, line)
		}
	}
}

func TestRenderScreenBufferFullFramePadsEmptyRenderToFullFrame(t *testing.T) {
	scr := newScreenBuffer(4, 2)
	out := renderScreenBufferFullFrame(scr, 4, 2)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != 4 {
			t.Fatalf("line %d width = %d, want 4; raw=%q", i, w, line)
		}
	}
}

func TestRenderScreenBufferFullFrameRoundTripsTrailingSpaces(t *testing.T) {
	src := newScreenBuffer(5, 1)
	line := src.Line(0)
	line[0] = uv.Cell{Content: "a", Width: 1}
	line[1] = uv.EmptyCell
	line[2] = uv.EmptyCell
	line[3] = uv.EmptyCell
	line[4] = uv.EmptyCell

	out := renderScreenBufferFullFrame(src, 5, 1)
	if w := ansi.StringWidth(out); w != 5 {
		t.Fatalf("serialized width = %d, want 5; raw=%q", w, out)
	}

	roundTrip := newScreenBuffer(5, 1)
	uv.NewStyledString(out).Draw(roundTrip, roundTrip.Bounds())
	for x := 0; x < 5; x++ {
		got := roundTrip.Line(0)[x]
		want := src.Line(0)[x]
		if !got.Equal(&want) {
			t.Fatalf("cell %d mismatch: got=%+v want=%+v", x, got, want)
		}
	}
}

func TestRenderScreenBufferFullFrameRoundTripsWideCells(t *testing.T) {
	src := newScreenBuffer(6, 1)
	uv.NewStyledString("a世b  ").Draw(src, src.Bounds())

	out := renderScreenBufferFullFrame(src, 6, 1)
	if w := ansi.StringWidth(out); w != 6 {
		t.Fatalf("serialized width = %d, want 6; raw=%q", w, out)
	}

	roundTrip := newScreenBuffer(6, 1)
	uv.NewStyledString(out).Draw(roundTrip, roundTrip.Bounds())
	for x := 0; x < 6; x++ {
		got := roundTrip.Line(0)[x]
		want := src.Line(0)[x]
		if !got.Equal(&want) {
			t.Fatalf("cell %d mismatch: got=%+v want=%+v raw=%q", x, got, want, out)
		}
	}
}

func TestModelViewDoesNotInjectUnsupportedControlSequencesWhenFocusResizeFreezeEnabled(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 60, 12)
	m.useFocusResizeFreeze = true
	v := m.View()
	// Full-frame serialization should rely on real cells/spaces rather than
	// terminal control sequences like CSI K, which UV StyledString doesn't interpret.
	if strings.Contains(v.Content, "\x1b[0K") {
		t.Fatalf("unexpected CSI 0K in View() output")
	}
}
