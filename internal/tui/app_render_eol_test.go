package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestPadRenderToFullFramePadsToWidthAndHeight(t *testing.T) {
	in := "abc\n\n"
	out := padRenderToFullFrame(in, 5, 3)
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

func TestPadRenderToFullFramePadsEmptyRenderToFullFrame(t *testing.T) {
	out := padRenderToFullFrame("", 4, 2)
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

func TestModelViewDoesNotInjectUnsupportedControlSequencesWhenFocusResizeFreezeEnabled(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 60, 12)
	m.useFocusResizeFreeze = true
	v := m.View()
	// padRenderToFullFrame should rely on printable padding rather than terminal
	// control sequences like CSI K, which UV StyledString doesn't interpret.
	if strings.Contains(v.Content, "\x1b[0K") {
		t.Fatalf("unexpected CSI 0K in View() output")
	}
}
