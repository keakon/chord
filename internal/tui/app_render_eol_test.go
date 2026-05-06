package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestModelViewAddsEraseToEOLWhenFocusResizeFreezeEnabled(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 60, 12)
	m.useFocusResizeFreeze = true
	v := m.View()
	if !strings.Contains(v.Content, ansiEraseToEOL) {
		t.Fatalf("expected View() output to contain %q when focus-resize freeze is enabled", ansiEraseToEOL)
	}

	m2 := NewModelWithSize(nil, 60, 12)
	m2.useFocusResizeFreeze = false
	v2 := m2.View()
	if strings.Contains(v2.Content, ansiEraseToEOL) {
		t.Fatalf("expected View() output to not contain %q when focus-resize freeze is disabled", ansiEraseToEOL)
	}
}
