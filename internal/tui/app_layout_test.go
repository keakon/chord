package tui

import "testing"

// TestRightPanelWidthConsistency locks the invariant that recalcViewportSize
// (which sizes the scrollable viewport) and generateLayout (which sizes the
// drawn rectangles) reserve exactly the same number of columns for the right
// panel. These two functions used to hardcode the panel width (32) and gap (1)
// independently; if they ever disagree, the viewport content width and the
// rectangle it is drawn into diverge, causing column misalignment or clipping.
func TestRightPanelWidthConsistency(t *testing.T) {
	const w, h = 140, 40
	m := NewModelWithSize(nil, w, h)
	m.mode = ModeNormal
	m.rightPanelVisible = true

	m.recalcViewportSize()
	layout := m.generateLayout(w, h)

	if m.viewport.width != layout.main.Dx() {
		t.Fatalf("viewport width (%d) != main layout width (%d): recalcViewportSize and generateLayout disagree on right-panel reservation",
			m.viewport.width, layout.main.Dx())
	}
	if got := layout.main.Max.X; got != w-rightPanelWidth-rightPanelGap {
		t.Fatalf("main right edge = %d, want %d (w - rightPanelWidth - rightPanelGap)", got, w-rightPanelWidth-rightPanelGap)
	}
	if layout.main.Max.X > layout.infoPanel.Min.X {
		t.Fatalf("main area overlaps the right panel: main.Max.X=%d infoPanel.Min.X=%d", layout.main.Max.X, layout.infoPanel.Min.X)
	}
}

// TestRightPanelHiddenUsesFullWidth verifies that when the panel is hidden the
// viewport and main rectangle both span the full terminal width with no
// reserved columns or gap.
func TestRightPanelHiddenUsesFullWidth(t *testing.T) {
	const w, h = 100, 30
	m := NewModelWithSize(nil, w, h)
	m.mode = ModeNormal
	m.rightPanelVisible = false

	m.recalcViewportSize()
	layout := m.generateLayout(w, h)

	if m.viewport.width != w {
		t.Fatalf("viewport width = %d, want full width %d when right panel hidden", m.viewport.width, w)
	}
	if layout.main.Dx() != w {
		t.Fatalf("main layout width = %d, want full width %d when right panel hidden", layout.main.Dx(), w)
	}
}

// TestRightPanelVisibilityHysteresis pins the show/hide threshold behaviour so
// the flicker-avoiding hysteresis band (116-119) keeps the current state.
func TestRightPanelVisibilityHysteresis(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)

	m.width = rightPanelShowMinWidth
	m.updateRightPanelVisible()
	if !m.rightPanelVisible {
		t.Fatalf("panel should be visible at width %d", rightPanelShowMinWidth)
	}

	// Inside the hysteresis band: state must not change.
	m.width = rightPanelHideMinWidth + 1
	m.updateRightPanelVisible()
	if !m.rightPanelVisible {
		t.Fatalf("panel should stay visible inside hysteresis band (width %d)", m.width)
	}

	m.width = rightPanelHideMinWidth - 1
	m.updateRightPanelVisible()
	if m.rightPanelVisible {
		t.Fatalf("panel should hide below width %d", rightPanelHideMinWidth)
	}

	// Back into the band from hidden: must stay hidden.
	m.width = rightPanelHideMinWidth + 1
	m.updateRightPanelVisible()
	if m.rightPanelVisible {
		t.Fatalf("panel should stay hidden inside hysteresis band (width %d)", m.width)
	}
}
