package tui

import (
	"image"

	"charm.land/lipgloss/v2"
	uv "github.com/keakon/ultraviolet"
)

const (
	// rightPanelWidth is the column width reserved for the right panel
	// (agent list + info panel) when it is visible.
	rightPanelWidth = 32
	// rightPanelGap is the 1-column visual gap between the main content
	// area and the right panel.
	rightPanelGap = 1
	// rightPanelShowMinWidth / rightPanelHideMinWidth implement visibility
	// hysteresis: the panel appears at >= show width and only hides again
	// below the hide width. The gap between them absorbs rapid intermediate
	// WindowSizeMsgs that would otherwise cause flicker.
	rightPanelShowMinWidth = 120
	rightPanelHideMinWidth = 116
	// minViewportWidth is the floor for the scrollable content viewport so it
	// stays usable even on very narrow terminals.
	minViewportWidth = 20
)

// inputAreaHeight returns the height of the input area (separator line + content lines + bottom margin).
func (m *Model) inputAreaHeight() int {
	if m.isViewingReadOnlySubAgent() {
		return 0
	}
	switch m.mode {
	case ModeSearch:
		return 1 // search bar only
	default:
		lines := m.input.ClampedDisplayLineCount()
		// separator(1) + content(lines) + bottom margin(1)
		return lines + 2
	}
}

// updateRightPanelVisible applies hysteresis to the right-panel visibility flag.
// The panel appears once width reaches rightPanelShowMinWidth, but is only hidden
// again when width drops below rightPanelHideMinWidth. This prevents flicker
// caused by terminals emitting rapid intermediate WindowSizeMsgs during startup
// or tab switches that briefly cross the threshold.
func (m *Model) updateRightPanelVisible() {
	if m.width >= rightPanelShowMinWidth {
		m.rightPanelVisible = true
	} else if m.width < rightPanelHideMinWidth {
		m.rightPanelVisible = false
	}
	// Between the hide and show widths: keep current state (hysteresis zone).
}

func (m *Model) recalcViewportSize() {
	vpHeight := m.height
	vpHeight -= m.inputAreaHeight()
	// Status bar always has its own row.
	vpHeight -= 1
	if !m.isViewingReadOnlySubAgent() {
		attachLines := min(len(m.attachments), 5)
		vpHeight -= attachLines
		queueLines := min(len(m.visibleQueuedDrafts()), 3)
		vpHeight -= queueLines
	}
	if m.activeToast != nil {
		vpHeight--
	}
	if vpHeight < 1 {
		vpHeight = 1
	}
	// Reduce viewport width when the right panel (info + agents) is visible.
	vpWidth := m.width
	if m.rightPanelVisible && m.mode != ModeHelp {
		vpWidth -= rightPanelWidth
		vpWidth -= rightPanelGap
	}
	if vpWidth < minViewportWidth {
		vpWidth = minViewportWidth
	}
	m.viewport.SetSize(vpWidth, vpHeight)
}

// generateLayout computes layout rectangles for layered drawing.
// Status bar always has its own bottom row; input area is above it.
func (m *Model) generateLayout(w, h int) tuiLayout {
	area := image.Rect(0, 0, w, h)
	lay := tuiLayout{area: area}

	inputHeight := m.inputAreaHeight()
	contentEnd := h

	// Status bar always occupies the bottom row.
	contentEnd -= 1
	lay.status = image.Rect(0, contentEnd, w, h)

	contentEnd -= inputHeight
	lay.input = image.Rect(0, contentEnd, w, contentEnd+inputHeight)

	// Attachment bar: 1 row per attachment (max 5), shown above input
	attachHeight := min(len(m.attachments), 5)
	if attachHeight > 0 && inputHeight > 0 {
		contentEnd -= attachHeight
		lay.attachments = image.Rect(0, contentEnd, w, contentEnd+attachHeight)
	}

	// Queue bar: 1 row per queued draft (max 3), shown above attachments/input
	queueHeight := min(len(m.visibleQueuedDrafts()), 3)
	if queueHeight > 0 {
		contentEnd -= queueHeight
		lay.queue = image.Rect(0, contentEnd, w, contentEnd+queueHeight)
	}

	// Toast: 1 row above input if active
	if m.activeToast != nil {
		contentEnd--
		lay.toast = image.Rect(0, contentEnd, w, contentEnd+1)
	}
	contentHeight := contentEnd

	// Content row: main | rightPanel
	// When visible, the right panel hosts both the agent list (sidebar) on
	// top and the info panel below. No left-side sidebar any more.
	panelWidth := 0
	gap := 0
	if m.rightPanelVisible && m.mode != ModeHelp {
		panelWidth = rightPanelWidth
		gap = rightPanelGap
	}
	mainMaxX := w - panelWidth - gap
	lay.main = image.Rect(0, 0, mainMaxX, contentHeight)
	lay.infoPanel = image.Rect(w-panelWidth, 0, w, contentHeight)
	return lay
}

func (m *Model) renderOverlayCached(scr uv.Screen, area image.Rectangle, cache *cachedRenderable, text string) {
	if text == "" || area.Dx() <= 0 || area.Dy() <= 0 {
		return
	}
	m.renderToCache(cache, text)
	m.drawCachedRenderable(scr, area, cache)
}

func centeredRect(container image.Rectangle, content string) image.Rectangle {
	cw := lipgloss.Width(content)
	ch := lipgloss.Height(content)
	if cw < 0 {
		cw = 0
	}
	if ch < 0 {
		ch = 0
	}
	if cw > container.Dx() {
		cw = container.Dx()
	}
	if ch > container.Dy() {
		ch = container.Dy()
	}
	x0 := container.Min.X + (container.Dx()-cw)/2
	y0 := container.Min.Y + (container.Dy()-ch)/2
	return image.Rect(x0, y0, x0+cw, y0+ch)
}

func (m *Model) ensureLayoutForHitTest() tuiLayout {
	if m.layout.main.Dx() <= 0 || m.layout.main.Dy() <= 0 {
		m.layout = m.generateLayout(m.width, m.height)
	}
	return m.layout
}

func (m *Model) overlayRect(content string) image.Rectangle {
	layout := m.ensureLayoutForHitTest()
	return centeredRect(layout.area, content)
}

func overlayItemIndexAt(dialogRect image.Rectangle, y, contentBaseRow, windowStart, visibleCount int) (int, bool) {
	if y < dialogRect.Min.Y || y >= dialogRect.Max.Y {
		return 0, false
	}
	contentY := y - dialogRect.Min.Y - 1 // skip top border
	row := contentY - contentBaseRow
	if row < 0 || row >= visibleCount {
		return 0, false
	}
	return windowStart + row, true
}

func (m *Model) sessionSelectOptionIndexAt(x, y int) (int, bool) {
	dialog := m.renderSessionSelectDialog()
	return m.sessionSelect.selector.IndexAt(m, dialog, x, y)
}

func (m *Model) poolSelectIndexAt(x, y int) (int, bool) {
	dialog := m.renderModelSelectDialog()
	idx, ok := m.modelSelect.selector.IndexAt(m, dialog, x, y)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= len(m.modelSelect.poolNames) {
		return 0, false
	}
	return idx, true
}

func (m *Model) handoffSelectOptionIndexAt(x, y int) (int, bool) {
	dialog := m.renderHandoffSelectDialog()
	idx, ok := m.handoffSelect.selector.IndexAt(m, dialog, x, y)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= len(m.handoffSelect.options) {
		return 0, false
	}
	return idx, true
}

func (m *Model) mcpSelectOptionIndexAt(x, y int) (int, bool) {
	dialog := m.renderMCPSelectDialog()
	return m.mcpSelect.selector.IndexAt(m, dialog, x, y)
}
