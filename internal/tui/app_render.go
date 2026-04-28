package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/screen"
	"github.com/charmbracelet/x/ansi"
)

// Draw draws the TUI onto the given screen using layered rendering: base layer
// first (sidebar, main, infoPanel, input, toast, status), then overlays (session/model
// dialogs, slash completion). Dialogs and completion are drawn last so they float on top.
func (m *Model) Draw(scr uv.Screen, area image.Rectangle) *tea.Cursor {
	w, h := area.Dx(), area.Dy()
	layout := m.generateLayout(w, h)
	m.layout = layout

	m.clearScreenBuffer(scr)

	if m.quitting {
		uv.NewStyledString("Goodbye!\n").Draw(scr, area)
		return nil
	}

	// ---- Base layer ----

	// Main: viewport, directory, or welcome (no dialogs here; those are overlay)
	viewportWidth := layout.main.Dx()
	viewportHeight := layout.main.Dy()
	if m.viewport != nil && (m.viewport.width != viewportWidth || m.viewport.height != viewportHeight) {
		m.recordTUIDiagnostic("viewport-layout-sync", "stale=%dx%d layout=%dx%d offset=%d mode=%s", m.viewport.width, m.viewport.height, viewportWidth, viewportHeight, debugViewportOffset(m.viewport), debugModeString(m.mode))
	}
	m.viewport.SetSize(viewportWidth, viewportHeight)
	var mainContent string
	spinnerFrame := m.activityFrame()
	selection := m.viewportSelectionPtr()
	searchBlockIndex := -1
	if m.search.State.Active {
		searchBlockIndex = m.searchCurrentBlockIndex()
	}
	mainKey := m.mainRenderKey(m.mode, viewportWidth)
	switch m.mode {
	case ModeDirectory:
		mainContent = m.renderDirectory()
	case ModeHelp:
		mainContent = m.renderHelpView()
	default:
		needMainRender := m.cachedMainKey != mainKey || m.cachedMainSpinnerFrame != spinnerFrame || m.cachedMainSearchBlockIndex != searchBlockIndex
		if selection == nil {
			needMainRender = needMainRender || m.cachedMainSelActive
		} else {
			needMainRender = needMainRender || !m.cachedMainSelActive || m.cachedMainSel != *selection
		}
		if needMainRender {
			mainContent = m.viewport.Render(spinnerFrame, selection, searchBlockIndex)
		} else {
			mainContent = m.cachedMainRender.text
		}
		// Show the welcome screen only when nothing has been rendered yet.
		// Do not key off agent message history here because the TUI appends
		// blocks immediately on user input.
		if len(m.viewport.blocks) == 0 {
			welcomeHints := []string{
				DimStyle.Render("Press ? for help · /help for commands"),
				DimStyle.Render("i: insert  /: search  ctrl+p: model  ctrl+j: directory"),
				DimStyle.Render("enter: send  esc: normal  ctrl+v: paste image"),
			}
			emptyMessage := DimStyle.Render("No messages yet. Start a conversation!")
			if m.startupRestorePending {
				emptyMessage = DimStyle.Render("Restoring session...")
			}
			welcome := lipgloss.Place(viewportWidth, m.viewport.height,
				lipgloss.Center, lipgloss.Center,
				lipgloss.JoinVertical(lipgloss.Center,
					HeaderStyle.Render(" CHORD "),
					"",
					emptyMessage,
					"",
					strings.Join(welcomeHints, "\n"),
				),
			)
			mainContent = welcome
		}
	}
	if layout.main.Dx() > 0 && layout.main.Dy() > 0 {
		if m.cachedMainKey != mainKey || m.cachedMainRender.text != mainContent {
			m.cachedMainKey = mainKey
			m.cachedMainSpinnerFrame = spinnerFrame
			m.cachedMainSearchBlockIndex = searchBlockIndex
			if selection == nil {
				m.cachedMainSelActive = false
				m.cachedMainSel = SelectionRange{}
			} else {
				m.cachedMainSelActive = true
				m.cachedMainSel = *selection
			}
			m.renderToCache(&m.cachedMainRender, mainContent)
		}
		m.drawCachedRenderableToClearedArea(scr, layout.main, &m.cachedMainRender)
	}

	// Info panel
	if layout.infoPanel.Dx() > 0 {
		infoView := m.renderInfoPanel(layout.infoPanel.Dx(), m.viewport.height)
		if m.cachedDirRender.text != infoView {
			m.renderToCache(&m.cachedDirRender, infoView)
		}
		m.drawCachedRenderableToClearedArea(scr, layout.infoPanel, &m.cachedDirRender)
	}

	// Sync textarea height (safety net; primary sync happens in Update paths).
	// Keep render-time height semantics aligned with update/layout logic: use
	// display lines (soft wraps included), not logical \n-split lines.
	if wantInputHeight := m.input.ClampedDisplayLineCount(); m.input.Height() != wantInputHeight {
		m.input.SetHeight(wantInputHeight)
	}

	inputSuppressed := m.interactionSuppressed()
	inputValue := m.input.DisplayValue()
	inputSelection := m.input.SelectionState()
	inputFocused := m.input.Focused()
	inputBangMode := m.input.BangMode()
	inputLine := m.input.Line()
	inputColumn := m.input.Column()
	inputScrollY := m.input.ScrollYOffset()
	inputAnimKey := m.inputAnimationCacheKeyAt(time.Now())
	searchInputArea := ""
	if m.mode == ModeSearch {
		searchWidth := m.width - 1
		if searchWidth < 1 {
			searchWidth = 1
		}
		searchInputArea = " " + m.search.View(searchWidth)
	}
	needInputRender := m.cachedInputKey == "" ||
		m.cachedInputMode != m.mode ||
		m.cachedInputWidth != m.width ||
		m.cachedInputHeight != layout.input.Dy() ||
		m.cachedInputSuppressed != inputSuppressed ||
		m.cachedInputSelectionAlive != !inputSelection.empty() ||
		m.cachedInputFocused != inputFocused ||
		m.cachedInputBangMode != inputBangMode ||
		m.cachedInputValue != inputValue ||
		m.cachedInputLine != inputLine ||
		m.cachedInputColumn != inputColumn ||
		m.cachedInputScrollY != inputScrollY ||
		m.cachedInputAnimKey != inputAnimKey ||
		m.cachedInputSelection != inputSelection ||
		(m.mode == ModeSearch && m.cachedInputRender.text != searchInputArea)
	if needInputRender {
		var inputArea string
		switch m.mode {
		case ModeInsert:
			sep := m.renderAnimatedInputSeparator(m.width)
			inputArea = sep + "\n" + m.input.ViewWithSelection() + "\n"
		case ModeSearch:
			inputArea = searchInputArea
		default:
			sep := m.renderAnimatedInputSeparator(m.width)
			inputArea = sep + "\n" + m.input.ViewWithSelection() + "\n"
		}
		if inputSuppressed {
			inputArea = renderDisabledInputArea(inputArea)
		}
		inputKey := fmt.Sprintf("%d|%d|%s|%d", m.mode, m.width, inputArea, layout.input.Dy())
		if m.cachedInputKey != inputKey || m.cachedInputRender.text != inputArea {
			m.cachedInputKey = inputKey
			m.renderToCache(&m.cachedInputRender, inputArea)
		}
		m.cachedInputMode = m.mode
		m.cachedInputWidth = m.width
		m.cachedInputHeight = layout.input.Dy()
		m.cachedInputSuppressed = inputSuppressed
		m.cachedInputSelectionAlive = !inputSelection.empty()
		m.cachedInputFocused = inputFocused
		m.cachedInputBangMode = inputBangMode
		m.cachedInputValue = inputValue
		m.cachedInputLine = inputLine
		m.cachedInputColumn = inputColumn
		m.cachedInputScrollY = inputScrollY
		m.cachedInputSelection = inputSelection
		m.cachedInputAnimKey = inputAnimKey
		m.cachedInputCursorOK = false
		if !inputSuppressed && (m.mode == ModeInsert || m.mode == ModeNormal) && inputFocused {
			if cur := m.input.Cursor(); cur != nil {
				m.cachedInputCursor = *cur
				m.cachedInputCursorOK = true
			}
		}
	}
	m.drawCachedRenderableToClearedArea(scr, layout.input, &m.cachedInputRender)

	// Attachment bar (above input)
	if layout.attachments.Dy() > 0 {
		attachmentsPresent := len(m.attachments) > 0
		if attachmentsPresent {
			attachKey := m.attachmentsFingerprint()
			if m.cachedAttachKey != attachKey || !m.cachedAttachmentsPresent {
				m.cachedAttachKey = attachKey
				m.cachedAttachmentsPresent = true
				m.renderToCache(&m.cachedAttachRender, m.renderAttachments())
			}
		} else if m.cachedAttachmentsPresent {
			m.cachedAttachmentsPresent = false
			m.cachedAttachKey = ""
			m.cachedAttachRender = cachedRenderable{}
		}
		m.drawCachedRenderableToClearedArea(scr, layout.attachments, &m.cachedAttachRender)
	}

	// Queued drafts bar (above attachments/input)
	if layout.queue.Dy() > 0 {
		queuePresent := len(m.visibleQueuedDrafts()) > 0 && layout.queue.Dx() > 0 && layout.queue.Dy() > 0
		if queuePresent {
			queueKey := m.queuedDraftsFingerprint(layout.queue.Dx(), layout.queue.Dy())
			if m.cachedQueueKey != queueKey || !m.cachedQueuePresent || m.cachedQueueWidth != layout.queue.Dx() || m.cachedQueueHeight != layout.queue.Dy() {
				m.cachedQueuePresent = true
				m.cachedQueueWidth = layout.queue.Dx()
				m.cachedQueueHeight = layout.queue.Dy()
				m.cachedQueueKey = queueKey
				m.renderToCache(&m.cachedQueueRender, m.renderQueuedDrafts(layout.queue.Dx(), layout.queue.Dy()))
			}
		} else if m.cachedQueuePresent {
			m.cachedQueuePresent = false
			m.cachedQueueWidth = 0
			m.cachedQueueHeight = 0
			m.cachedQueueKey = ""
			m.cachedQueueRender = cachedRenderable{}
		}
		m.drawCachedRenderableToClearedArea(scr, layout.queue, &m.cachedQueueRender)
	}

	// Toast
	if m.activeToast != nil && layout.toast.Dy() > 0 {
		toastView := m.renderToast()
		toastKey := m.toastFingerprint()
		if m.cachedToastKey != toastKey || m.cachedToastRender.text != toastView {
			m.cachedToastKey = toastKey
			m.renderToCache(&m.cachedToastRender, toastView)
		}
		m.drawCachedRenderable(scr, layout.toast, &m.cachedToastRender)
	}

	// Status bar always at the bottom row.
	if layout.status.Dy() > 0 {
		now := time.Now()
		statusKey := m.statusBarFingerprint(now)
		needStatusRender := m.cachedStatusKey != statusKey
		if needStatusRender {
			statusView := m.renderStatusBar()
			m.cachedStatusKey = statusKey
			m.renderToCache(&m.cachedStatusRender, statusView)
		}
		m.drawCachedRenderableToClearedArea(scr, layout.status, &m.cachedStatusRender)
	}

	// ---- Overlay layer (drawn last so they float on top) ----
	// Dialogs use full-screen bounds (area) so they overlay everything
	// (sidebar, infoPanel, etc.) so they appear on top of all other layers.

	switch m.mode {
	case ModeHandoffSelect:
		dialog := m.renderHandoffSelectDialog()
		dialogRect := centeredRect(area, dialog)
		m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
	case ModeSessionSelect:
		dialog := m.renderSessionSelectDialog()
		dialogRect := centeredRect(area, dialog)
		m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
	case ModeSessionDeleteConfirm:
		if dialog := m.renderSessionDeleteConfirmDialog(); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
		}
	case ModeModelSelect:
		dialog := m.renderModelSelectDialog()
		dialogRect := centeredRect(area, dialog)
		m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
	case ModeUsageStats:
		if dialog := m.renderUsageStatsDialog(); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
		}
	case ModeConfirm:
		if dialog := m.renderConfirmDialog(); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
		}
	case ModeQuestion:
		if dialog := m.renderQuestionDialog(); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
		}
	case ModeImageViewer:
		if dialog := m.renderImageViewerOverlay(); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
		}
	case ModeRules:
		if dialog := m.renderRulesList(); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedDirRender, dialog)
		}
	}
	if m.sessionSwitch.active() {
		if dialog := m.renderSessionSwitchOverlay(area); dialog != "" {
			dialogRect := centeredRect(area, dialog)
			m.renderOverlayCached(scr, dialogRect, &m.cachedHelpRender, dialog)
		}
	}

	// Slash completion: draw absolutely last, above all other dialogs/panels
	if m.mode == ModeInsert {
		if drop := m.renderSlashCompletionDropdown(inputValue); drop != "" {
			// Slash completion: draw at bottom of main area
			dropLines := strings.Count(drop, "\n") + 1
			dy := layout.main.Dy()
			if dropLines <= dy {
				y0 := layout.main.Max.Y - dropLines
				// Keep within main area so it doesn't overlap the info panel/sidebar.
				dropRect := image.Rect(layout.main.Min.X, y0, layout.main.Max.X, layout.main.Max.Y)
				uv.NewStyledString(drop).Draw(scr, dropRect)
			}
		}
		// @ mention file completion popup
		if m.atMentionOpen && m.atMentionList != nil && m.atMentionList.Len() > 0 {
			popupWidth := min(50, m.width/2)
			popup := m.atMentionList.Render(popupWidth)
			popupHeight := lipgloss.Height(popup)
			x := layout.input.Min.X
			y := layout.input.Min.Y - popupHeight
			if y < 0 {
				y = 0
			}
			popupRect := image.Rect(x, y, x+popupWidth, y+popupHeight)
			uv.NewStyledString(popup).Draw(scr, popupRect)
		}
	}

	// Cursor for input focus: textarea returns position relative to its content;
	// add layout offset. No border/padding: Y offset is separator line (1), X has no extra padding.
	if m.cachedInputCursorOK {
		cur := m.cachedInputCursor
		cur.Y += layout.input.Min.Y + 1 // separator line(1)
		cur.X += layout.input.Min.X
		return &cur
	}
	return nil
}

// View renders the entire screen (Bubble Tea v2: returns tea.View with Content from buffer).
func (m *Model) View() tea.View {
	if m.shouldFreezeRender() {
		return m.cachedFrozenView
	}
	if m.shouldDeferStreamRender() {
		return m.cachedFullView
	}

	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true

	if m.quitting {
		v.Content = "Goodbye!\n"
		m.cachedFullView = v
		m.cachedFullViewValid = true
		m.streamRenderForceView = false
		m.streamRenderDeferred = false
		m.streamRenderDeferNext = false
		return v
	}

	m.ensureScreenBuffer(m.width, m.height)
	canvas := m.screenBuf
	v.Cursor = m.Draw(canvas, canvas.Bounds())

	v.Content = canvas.Render()
	m.cachedFullView = v
	m.cachedFullViewValid = true
	if m.renderFreezeActive {
		m.cachedFrozenView = v
		m.cachedFrozenViewValid = true
	}
	m.streamRenderForceView = false
	m.streamRenderDeferred = m.streamRenderDeferNext
	m.streamRenderDeferNext = false
	return v
}

// ensureScreenBuffer reuses the existing UV screen buffer across View() calls.
// Reallocating the whole buffer every frame was unnecessary churn once higher-
// level layout work had been cached; keeping one buffer and resizing in place
// reduces per-frame allocations and keeps cached draw paths effective.
func (m *Model) ensureScreenBuffer(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	if m.screenBuf.RenderBuffer == nil {
		m.screenBuf = newScreenBuffer(width, height)
		m.refreshScreenBlankLine(width)
		return
	}
	m.screenBuf.Method = ansi.GraphemeWidth
	m.screenBuf.Resize(width, height)
	m.refreshScreenBlankLine(width)
}

func (m *Model) refreshScreenBlankLine(width int) {
	if width < 0 {
		width = 0
	}
	if len(m.screenBlankLine) == width {
		return
	}
	m.screenBlankLine = make(uv.Line, width)
	for i := range m.screenBlankLine {
		m.screenBlankLine[i] = uv.EmptyCell
	}
}

func (m *Model) clearScreenBuffer(scr uv.Screen) {
	type lineAccessor interface {
		Line(y int) uv.Line
	}
	lines, ok := scr.(lineAccessor)
	if !ok {
		screen.Clear(scr)
		return
	}
	bounds := scr.Bounds()
	width := bounds.Dx()
	if len(m.screenBlankLine) != width {
		m.refreshScreenBlankLine(width)
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		line := lines.Line(y)
		if line == nil {
			continue
		}
		copy(line, m.screenBlankLine)
	}
}

// newScreenBuffer creates a screen buffer whose width accounting matches the
// rest of the TUI rendering pipeline. GraphemeWidth correctly handles emoji
// clusters and odd sequences like "\u200d♀️" without collapsing them to one cell.
func newScreenBuffer(width, height int) uv.ScreenBuffer {
	canvas := uv.NewScreenBuffer(width, height)
	canvas.Method = ansi.GraphemeWidth
	return canvas
}
