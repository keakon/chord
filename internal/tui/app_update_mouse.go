package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

const mouseWheelScrollStep = 3

func (m *Model) handleModalMouseMsg(msg tea.MouseMsg) (tea.Cmd, bool) {
	mouse := msg.Mouse()

	// Session select overlay (modal): wheel scrolls list.
	if m.mode == ModeSessionSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.sessionSelect.selector.list != nil {
				m.sessionSelect.selector.list.HandleWheel(-mouseWheelScrollStep)
			}
			return nil, true
		case tea.MouseWheelDown:
			if m.sessionSelect.selector.list != nil {
				m.sessionSelect.selector.list.HandleWheel(mouseWheelScrollStep)
			}
			return nil, true
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.sessionSelectOptionIndexAt(mouse.X, mouse.Y); ok {
				if m.sessionSelect.selector.list != nil {
					m.sessionSelect.selector.list.SetCursor(idx)
				}
				return m.selectSessionAtCursor(), true
			}
		}
		return nil, true
	}

	if m.mode == ModeSessionDeleteConfirm {
		m.clearChordState()
		return nil, true
	}

	if m.mode == ModeModelSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.modelSelect.selector.list != nil {
				m.modelSelect.selector.list.HandleWheel(-mouseWheelScrollStep)
				m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
			} else if m.modelSelect.poolCursor > 0 {
				m.modelSelect.poolCursor--
			}
			return nil, true
		case tea.MouseWheelDown:
			if m.modelSelect.selector.list != nil {
				m.modelSelect.selector.list.HandleWheel(mouseWheelScrollStep)
				m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
			} else if len(m.modelSelect.poolNames) > 0 && m.modelSelect.poolCursor < len(m.modelSelect.poolNames)-1 {
				m.modelSelect.poolCursor++
			}
			return nil, true
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.poolSelectIndexAt(mouse.X, mouse.Y); ok {
				if m.modelSelect.selector.list != nil {
					m.modelSelect.selector.list.SetCursor(idx)
					m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
				} else {
					m.modelSelect.poolCursor = idx
				}
				return m.selectPoolAtCursor(), true
			}
		}
		return nil, true
	}

	if m.mode == ModeMCPSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.mcpSelect.selector.list != nil {
				m.mcpSelect.selector.list.HandleWheel(-mouseWheelScrollStep)
			}
			return nil, true
		case tea.MouseWheelDown:
			if m.mcpSelect.selector.list != nil {
				m.mcpSelect.selector.list.HandleWheel(mouseWheelScrollStep)
			}
			return nil, true
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.mcpSelectOptionIndexAt(mouse.X, mouse.Y); ok {
				return m.mcpSelectToggleAtIndex(idx), true
			}
		}
		return nil, true
	}

	if m.mode == ModeHandoffSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.handoffSelect.scroll -= mouseWheelScrollStep
			if m.handoffSelect.scroll < 0 {
				m.handoffSelect.scroll = 0
			}
			return nil, true
		case tea.MouseWheelDown:
			m.handoffSelect.scroll += mouseWheelScrollStep
			return nil, true
		}
		if m.handoffSelect.denyingWithReason {
			return nil, true
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.handoffSelectOptionIndexAt(mouse.X, mouse.Y); ok {
				if m.handoffSelect.selector.list != nil {
					m.handoffSelect.selector.list.SetCursor(idx)
				}
				return m.confirmHandoff(), true
			}
		}
		return nil, true
	}

	if m.mode == ModeContentViewer {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.scrollContentViewer(-mouseWheelScrollStep)
			return nil, true
		case tea.MouseWheelDown:
			m.scrollContentViewer(mouseWheelScrollStep)
			return nil, true
		}
		switch msg.(type) {
		case tea.MouseClickMsg:
			if mouse.Button == tea.MouseLeft {
				m.startContentViewerSelection(mouse)
			}
		case tea.MouseMotionMsg:
			m.updateContentViewerSelection(mouse)
		case tea.MouseReleaseMsg:
			if mouse.Button == tea.MouseLeft {
				m.contentViewer.selecting = false
			}
		}
		return nil, true
	}

	// Confirm/Question/Rules/UsageStats/Help overlay modes keep clicks from
	// passing through, but allow wheel scrolling of the underlying viewport so
	// long background cards remain readable while the overlay is open.
	if m.mode == ModeConfirm || m.mode == ModeQuestion || m.mode == ModeRules || m.mode == ModeUsageStats || m.mode == ModeHelp {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp, tea.MouseWheelDown:
			return m.handleMouseWheel(mouse), true
		}
		return nil, true
	}
	if m.mode == ModeImageViewer {
		m.clearChordState()
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			rect, _ := m.imageViewerOverlayRect()
			if !rect.Empty() {
				outside := mouse.X < rect.Min.X || mouse.X >= rect.Max.X ||
					mouse.Y < rect.Min.Y || mouse.Y >= rect.Max.Y
				if outside {
					closeCmd := m.closeImageViewer()
					if closeCmd != nil {
						return closeCmd, true
					}
					return nil, true
				}
			}
		}
		return nil, true
	}

	return nil, false
}

func (m *Model) handleStatusCopyClick(x, y int) (tea.Cmd, bool) {
	if m.statusSessionContainsPoint(x, y) {
		return m.handleStatusClipboardClick(x, y, m.statusSession.value, writeStatusSessionClipboardCmd), true
	}
	if m.statusPathContainsPoint(x, y) {
		return m.handleStatusClipboardClick(x, y, m.statusPath.value, writeStatusPathClipboardCmd), true
	}
	return nil, false
}

func (m *Model) handleStatusClipboardClick(x, y int, value string, copyCmd func(string) tea.Cmd) tea.Cmd {
	m.clearFocusedBlock()
	now := time.Now()
	const doubleClickThreshold = 400 * time.Millisecond
	const clickTolerance = 2
	if now.Sub(m.statusPathLastClickTime) <= doubleClickThreshold &&
		abs(x-m.statusPathLastClickX) <= clickTolerance &&
		abs(y-m.statusPathLastClickY) <= clickTolerance {
		m.statusPathClickCount++
	} else {
		m.statusPathClickCount = 1
	}
	m.statusPathLastClickTime = now
	m.statusPathLastClickX = x
	m.statusPathLastClickY = y
	m.clearMouseSelection()
	m.input.ClearSelection()
	m.inputMouseDown = false
	if m.statusPathClickCount >= 2 {
		m.statusPathClickCount = 0
		return copyCmd(value)
	}
	return nil
}

type mouseHitZones struct {
	viewportLineRaw int
	viewportColRaw  int
	viewportLine    int
	viewportCol     int
	inViewport      bool
	inInputZone     bool
	inQueueZone     bool
	inInfoPanel     bool
}

func (m *Model) mouseHitZones(mouse tea.Mouse) mouseHitZones {
	viewportLineRaw := mouse.Y
	viewportColRaw := mouse.X
	viewportLine := viewportLineRaw
	viewportCol := viewportColRaw + 1
	if viewportLine < 0 {
		viewportLine = 0
	}
	if viewportLine >= m.viewport.height {
		viewportLine = m.viewport.height - 1
	}
	if viewportCol < 0 {
		viewportCol = 0
	}
	if viewportCol >= m.viewport.width {
		viewportCol = m.viewport.width - 1
	}
	return mouseHitZones{
		viewportLineRaw: viewportLineRaw,
		viewportColRaw:  viewportColRaw,
		viewportLine:    viewportLine,
		viewportCol:     viewportCol,
		inViewport: viewportLineRaw >= 0 && viewportLineRaw < m.viewport.height &&
			viewportColRaw >= 0 && viewportColRaw < m.viewport.width,
		inInputZone: m.layout.input.Dx() > 0 &&
			mouse.X >= m.layout.input.Min.X && mouse.X < m.layout.input.Max.X &&
			mouse.Y >= m.layout.input.Min.Y && mouse.Y < m.layout.input.Max.Y,
		inQueueZone: m.layout.queue.Dx() > 0 &&
			mouse.X >= m.layout.queue.Min.X && mouse.X < m.layout.queue.Max.X &&
			mouse.Y >= m.layout.queue.Min.Y && mouse.Y < m.layout.queue.Max.Y,
		inInfoPanel: m.infoPanelContainsPoint(mouse.X, mouse.Y),
	}
}

func (m *Model) handleMouseMsg(msg tea.MouseMsg) tea.Cmd {
	if m.interactionSuppressed() {
		return nil
	}

	if cmd, handled := m.handleModalMouseMsg(msg); handled {
		return cmd
	}

	mouse := msg.Mouse()
	hits := m.mouseHitZones(mouse)
	switch msg.(type) {
	case tea.MouseClickMsg:
		return m.handleMouseClick(mouse, hits)
	case tea.MouseMotionMsg:
		return m.handleMouseMotion(mouse, hits)
	case tea.MouseReleaseMsg:
		return m.handleMouseRelease(mouse)
	case tea.MouseWheelMsg:
		return m.handleMouseWheel(mouse)
	}
	return nil
}

func (m *Model) handleMouseClick(mouse tea.Mouse, hits mouseHitZones) tea.Cmd {
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	m.clearChordState()
	if hits.inQueueZone {
		return m.handleQueueMouseClick(mouse)
	}
	if hits.inInfoPanel {
		return m.handleInfoPanelMouseClick(mouse)
	}
	if cmd, handled := m.handleStatusCopyClick(mouse.X, mouse.Y); handled {
		return cmd
	}
	if cmd, handled := m.handleInputZoneMouseClick(mouse, hits); handled {
		return cmd
	}
	if hits.inViewport {
		return m.handleViewportMouseClick(mouse, hits)
	}
	return m.handleOutsideViewportMouseClick(mouse)
}

func (m *Model) handleQueueMouseClick(mouse tea.Mouse) tea.Cmd {
	m.clearFocusedBlock()
	if idx, remove, ok := m.queuedDraftActionAt(mouse.X, mouse.Y); ok {
		if remove {
			return m.deleteQueuedDraftAt(idx)
		}
		return m.editQueuedDraftAt(idx)
	}
	return nil
}

func (m *Model) handleInfoPanelMouseClick(mouse tea.Mouse) tea.Cmd {
	m.clearFocusedBlock()
	m.clearMouseSelection()
	m.input.ClearSelection()
	m.inputMouseDown = false
	if agentID, ok := m.infoPanelAgentAtPoint(mouse.X, mouse.Y); ok {
		if agentID == "main" {
			agentID = ""
		}
		m.setFocusedAgent(agentID)
		m.recalcViewportSize()
		return m.restartStatusBarTick()
	}
	if section, ok := m.infoPanelSectionAtPoint(mouse.X, mouse.Y); ok {
		m.toggleInfoPanelSection(section)
	}
	return nil
}

func (m *Model) handleInputZoneMouseClick(mouse tea.Mouse, hits mouseHitZones) (tea.Cmd, bool) {
	// Insert mode: click outside input zone -> switch to Normal and blur,
	// then fall through to start selection so drag works immediately.
	if m.mode == ModeInsert {
		if !hits.inInputZone {
			cmd := m.switchModeWithIME(ModeNormal)
			m.recalcViewportSize()
			if cmd != nil {
				return cmd, true
			}
			return nil, false
		}
		if line, col, ok := m.input.SelectionPointAt(
			mouse.Y-m.layout.input.Min.Y-1,
			mouse.X-m.layout.input.Min.X-inputPromptWidth,
		); ok {
			m.clearMouseSelection()
			m.input.StartSelection(line, col)
			m.inputMouseDown = true
		} else {
			m.input.ClearSelection()
			m.inputMouseDown = false
		}
		return nil, true
	}
	// Normal mode: click in input zone -> switch to Insert and focus.
	if m.mode == ModeNormal && hits.inInputZone {
		m.switchModeWithIME(ModeInsert)
		m.recalcViewportSize()
		m.clearFocusedBlock()
		if line, col, ok := m.input.SelectionPointAt(
			mouse.Y-m.layout.input.Min.Y-1,
			mouse.X-m.layout.input.Min.X-inputPromptWidth,
		); ok {
			m.clearMouseSelection()
			m.input.StartSelection(line, col)
			m.inputMouseDown = true
		} else {
			m.input.ClearSelection()
			m.inputMouseDown = false
		}
		return m.input.Focus(), true
	}
	return nil, false
}

func (m *Model) handleViewportMouseClick(mouse tea.Mouse, hits mouseHitZones) tea.Cmd {
	m.input.ClearSelection()
	m.inputMouseDown = false
	block, lineInBlock := m.viewportResolveMouse(hits.viewportLine)
	if block == nil {
		return nil
	}
	col := clampCol(hits.viewportCol, m.viewport.width)
	if cmd, handled := m.handleViewportSelectionClick(mouse, block, lineInBlock, col); handled {
		return cmd
	}
	return nil
}

func (m *Model) handleViewportSelectionClick(mouse tea.Mouse, block *Block, lineInBlock, col int) (tea.Cmd, bool) {
	// Double/triple click for word/line selection.
	const doubleClickThreshold = 400 * time.Millisecond
	const clickTolerance = 2
	now := time.Now()
	if now.Sub(m.lastClickTime) <= doubleClickThreshold &&
		abs(mouse.X-m.lastClickX) <= clickTolerance &&
		abs(mouse.Y-m.lastClickY) <= clickTolerance {
		m.clickCount++
	} else {
		m.clickCount = 1
	}
	m.lastClickTime = now
	m.lastClickX = mouse.X
	m.lastClickY = mouse.Y

	if m.clickCount == 2 {
		plain, _ := m.viewport.GetLinePlain(block.ID, lineInBlock)
		sCol, eCol := WordBoundsAtCol(plain, col)
		if sCol < eCol {
			m.selStartBlockID = block.ID
			m.selStartLine = lineInBlock
			m.selStartCol = sCol
			m.selEndBlockID = block.ID
			m.selEndLine = lineInBlock
			m.selEndCol = eCol
			m.selEndInclusiveForCopy = false
			m.mouseDown = false
		} else {
			m.startPointSelection(block.ID, lineInBlock, col)
		}
		return nil, true
	}
	if m.clickCount >= 3 {
		_, lineWidth := m.viewport.GetLinePlain(block.ID, lineInBlock)
		m.clickCount = 0
		if lineWidth > 0 {
			m.selStartBlockID = block.ID
			m.selStartLine = lineInBlock
			m.selStartCol = 0
			m.selEndBlockID = block.ID
			m.selEndLine = lineInBlock
			m.selEndCol = lineWidth
			m.selEndInclusiveForCopy = false
			m.mouseDown = false
		} else {
			// Empty line: treat as single click (point selection).
			m.startPointSelection(block.ID, lineInBlock, col)
		}
		return nil, true
	}

	if block.ID != m.focusedBlockID {
		m.setFocusedViewportBlock(block)
		// Clicking a Delegate block that has a linked subagent switches to that agent's view.
		m.maybeSwitchToTaskAgent(block)
		if part, ok := block.imagePartAtPoint(lineInBlock, col, m.viewport.width); ok {
			m.clearMouseSelection()
			m.mouseDown = false
			return openBlockImageCmd(m.runtimeImageOpenDir(), block, part, m.imageCaps), true
		}
		// Do not scroll on click; only j/k/g/G etc. reposition the view.
	} else {
		// Already focused: clicking again on a Delegate block still switches to subagent view.
		m.maybeSwitchToTaskAgent(block)
		if part, ok := block.imagePartAtPoint(lineInBlock, col, m.viewport.width); ok {
			m.clearMouseSelection()
			m.mouseDown = false
			return openBlockImageCmd(m.runtimeImageOpenDir(), block, part, m.imageCaps), true
		}
	}
	m.startPointSelection(block.ID, lineInBlock, col)
	return nil, true
}

func (m *Model) startPointSelection(blockID, line, col int) {
	m.mouseDown = true
	m.selStartBlockID = blockID
	m.selStartLine = line
	m.selStartCol = col
	m.selEndBlockID = blockID
	m.selEndLine = line
	m.selEndCol = col
	m.selEndInclusiveForCopy = true
}

func (m *Model) setFocusedViewportBlock(block *Block) {
	if m.focusedBlockID >= 0 {
		for _, b := range m.viewport.blocks {
			if b.ID == m.focusedBlockID {
				b.Focused = false
				b.InvalidateCache()
				break
			}
		}
	}
	if block != nil && isSelectableBlockType(block.Type) {
		m.focusedBlockID = block.ID
		block.Focused = true
		block.InvalidateCache()
		return
	}
	m.focusedBlockID = -1
}

func (m *Model) handleOutsideViewportMouseClick(mouse tea.Mouse) tea.Cmd {
	m.input.ClearSelection()
	m.inputMouseDown = false
	m.clearFocusedBlock()
	// Outside viewport: resolve focus from zones (e.g. directory overlay).
	// Zone bounds are from last Scan; we don't pass v2 MouseMsg to bubblezone.
	// Use viewport resolution for blocks (already have inViewport coords).
	for _, b := range m.viewport.blocks {
		idStr := fmt.Sprintf("block-%d", b.ID)
		z := m.zone.Get(idStr)
		if z.IsZero() || mouse.X < z.StartX || mouse.X > z.EndX || mouse.Y < z.StartY || mouse.Y > z.EndY {
			continue
		}
		if b.ID != m.focusedBlockID {
			m.setFocusedViewportBlock(b)
			m.maybeSwitchToTaskAgent(b)
		}
		break
	}
	return nil
}

func (m *Model) handleMouseMotion(mouse tea.Mouse, hits mouseHitZones) tea.Cmd {
	if m.inputMouseDown && hits.inInputZone {
		if line, col, ok := m.input.SelectionPointAt(
			mouse.Y-m.layout.input.Min.Y-1,
			mouse.X-m.layout.input.Min.X-inputPromptWidth,
		); ok {
			m.input.UpdateSelection(line, col)
		}
		return nil
	}
	if m.mouseDown && hits.inViewport {
		if block, lineInBlock := m.viewportResolveMouse(hits.viewportLine); block != nil {
			m.selEndBlockID = block.ID
			m.selEndLine = lineInBlock
			m.selEndCol = clampCol(hits.viewportCol, m.viewport.width)
			m.selEndInclusiveForCopy = true
		}
		return nil
	}
	return nil
}

func (m *Model) handleMouseRelease(mouse tea.Mouse) tea.Cmd {
	if mouse.Button == tea.MouseLeft {
		m.mouseDown = false
		m.inputMouseDown = false
	}
	return nil
}

func (m *Model) handleMouseWheel(mouse tea.Mouse) tea.Cmd {
	switch mouse.Button {
	case tea.MouseWheelUp:
		m.pendingScrollDelta -= mouseWheelScrollStep
		return m.scheduleScrollFlush(16 * time.Millisecond)
	case tea.MouseWheelDown:
		m.pendingScrollDelta += mouseWheelScrollStep
		return m.scheduleScrollFlush(16 * time.Millisecond)
	default:
		return nil
	}
}
