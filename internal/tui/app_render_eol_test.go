package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"
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

	m.renderFreezeActive = true
	m.cachedFrozenView = tea.View{Content: "frozen"}
	m.cachedFrozenViewValid = true
	frozen := m.View()
	if frozen.Content != "frozen" {
		t.Fatalf("frozen View.Content = %q, want frozen", frozen.Content)
	}
	if frozen.WindowTitle != "CachedTitle" {
		t.Fatalf("frozen View.WindowTitle = %q, want %q", frozen.WindowTitle, "CachedTitle")
	}

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
