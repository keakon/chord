package tui

import (
	"image"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
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
// The panel appears once width reaches 120 cols, but is only hidden again when
// width drops below 116. This prevents flicker caused by terminals emitting
// rapid intermediate WindowSizeMsgs during startup or tab switches that briefly
// cross the threshold.
func (m *Model) updateRightPanelVisible() {
	if m.width >= 120 {
		m.rightPanelVisible = true
	} else if m.width < 116 {
		m.rightPanelVisible = false
	}
	// Between 116 and 119: keep current state (hysteresis zone).
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
	if m.rightPanelVisible && m.mode != ModeDirectory && m.mode != ModeHelp {
		vpWidth -= 32 // right panel width
		vpWidth--     // 1-column gap before right panel
	}
	if vpWidth < 20 {
		vpWidth = 20
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
	// The right panel (width >= 120) hosts both the agent list (sidebar) on
	// top and the info panel below. No left-side sidebar any more.
	rightPanelWidth := 0
	if m.rightPanelVisible && m.mode != ModeDirectory && m.mode != ModeHelp {
		rightPanelWidth = 32
	}
	mainMaxX := w - rightPanelWidth
	if rightPanelWidth > 0 {
		mainMaxX-- // 1-column visual gap before right panel
	}
	lay.main = image.Rect(0, 0, mainMaxX, contentHeight)
	lay.infoPanel = image.Rect(w-rightPanelWidth, 0, w, contentHeight)
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
	if m.sessionSelect.list == nil || m.sessionSelect.list.Len() == 0 {
		return 0, false
	}
	dialogRect := m.overlayRect(m.renderSessionSelectDialog())
	if x < dialogRect.Min.X || x >= dialogRect.Max.X || y < dialogRect.Min.Y || y >= dialogRect.Max.Y {
		return 0, false
	}
	start, end := m.sessionSelect.list.WindowRange()
	idx, ok := overlayItemIndexAt(dialogRect, y, sessionSelectListBaseRow, start, end-start)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= m.sessionSelect.list.Len() {
		return 0, false
	}
	return idx, true
}

func (m *Model) modelSelectOptionIndexAt(x, y int) (int, bool) {
	if len(m.modelSelect.options) == 0 || m.modelSelect.table == nil {
		return 0, false
	}
	dialogRect := m.overlayRect(m.renderModelSelectDialog())
	if x < dialogRect.Min.X || x >= dialogRect.Max.X || y < dialogRect.Min.Y || y >= dialogRect.Max.Y {
		return 0, false
	}
	start, end := m.modelSelect.table.WindowRange()
	idx, ok := overlayItemIndexAt(dialogRect, y, 5, start, end-start)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= len(m.modelSelect.options) {
		return 0, false
	}
	if m.modelSelect.options[idx].Header {
		return 0, false
	}
	return idx, true
}

func (m *Model) handoffSelectOptionIndexAt(x, y int) (int, bool) {
	if len(m.handoffSelect.options) == 0 || m.handoffSelect.list == nil {
		return 0, false
	}
	dialogRect := m.overlayRect(m.renderHandoffSelectDialog())
	if x < dialogRect.Min.X || x >= dialogRect.Max.X || y < dialogRect.Min.Y || y >= dialogRect.Max.Y {
		return 0, false
	}
	start, end := m.handoffSelect.list.WindowRange()
	idx, ok := overlayItemIndexAt(dialogRect, y, 3, start, end-start)
	if !ok {
		return 0, false
	}
	if idx < 0 || idx >= len(m.handoffSelect.options) {
		return 0, false
	}
	return idx, true
}
