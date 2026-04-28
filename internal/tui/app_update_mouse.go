package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleMouseMsg(msg tea.MouseMsg) tea.Cmd {
	if m.interactionSuppressed() {
		return nil
	}

	mouse := msg.Mouse()
	viewportLineRaw := mouse.Y
	viewportColRaw := mouse.X
	inViewport := viewportLineRaw >= 0 && viewportLineRaw < m.viewport.height &&
		viewportColRaw >= 0 && viewportColRaw < m.viewport.width
	inInputZone := m.layout.input.Dx() > 0 &&
		mouse.X >= m.layout.input.Min.X && mouse.X < m.layout.input.Max.X &&
		mouse.Y >= m.layout.input.Min.Y && mouse.Y < m.layout.input.Max.Y
	inQueueZone := m.layout.queue.Dx() > 0 &&
		mouse.X >= m.layout.queue.Min.X && mouse.X < m.layout.queue.Max.X &&
		mouse.Y >= m.layout.queue.Min.Y && mouse.Y < m.layout.queue.Max.Y
	inInfoPanel := m.infoPanelContainsPoint(mouse.X, mouse.Y)

	// Session select overlay (modal): wheel scrolls list
	if m.mode == ModeSessionSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.sessionSelect.list != nil {
				m.sessionSelect.list.HandleWheel(-3)
			}
			return nil
		case tea.MouseWheelDown:
			if m.sessionSelect.list != nil {
				m.sessionSelect.list.HandleWheel(3)
			}
			return nil
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.sessionSelectOptionIndexAt(mouse.X, mouse.Y); ok {
				if m.sessionSelect.list != nil {
					m.sessionSelect.list.SetCursor(idx)
				}
				return m.selectSessionAtCursor()
			}
		}
		return nil
	}

	if m.mode == ModeSessionDeleteConfirm {
		m.clearChordState()
		return nil
	}

	if m.mode == ModeModelSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.modelSelect.table != nil {
				m.modelSelect.table.HandleWheel(-3)
			}
			return nil
		case tea.MouseWheelDown:
			if m.modelSelect.table != nil {
				m.modelSelect.table.HandleWheel(3)
			}
			return nil
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.modelSelectOptionIndexAt(mouse.X, mouse.Y); ok {
				if m.modelSelect.table != nil {
					m.modelSelect.table.list.SetCursor(idx)
				}
				return m.selectModelAtCursor()
			}
		}
		return nil
	}

	if m.mode == ModeHandoffSelect {
		m.clearChordState()
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.handoffSelect.list != nil {
				m.handoffSelect.list.HandleWheel(-3)
			}
			return nil
		case tea.MouseWheelDown:
			if m.handoffSelect.list != nil {
				m.handoffSelect.list.HandleWheel(3)
			}
			return nil
		}
		if _, isClick := msg.(tea.MouseClickMsg); isClick && mouse.Button == tea.MouseLeft {
			if idx, ok := m.handoffSelectOptionIndexAt(mouse.X, mouse.Y); ok {
				if m.handoffSelect.list != nil {
					m.handoffSelect.list.SetCursor(idx)
				}
				return m.confirmHandoff()
			}
		}
		return nil
	}

	// Confirm/Question/Rules/UsageStats/Help overlay modes: consume all mouse events to prevent
	// clicks from passing through to underlying components.
	if m.mode == ModeConfirm || m.mode == ModeQuestion || m.mode == ModeRules || m.mode == ModeUsageStats || m.mode == ModeHelp {
		m.clearChordState()
		return nil
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
						return closeCmd
					}
					return nil
				}
			}
		}
		return nil
	}

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

	switch msg.(type) {
	case tea.MouseClickMsg:
		if mouse.Button == tea.MouseLeft {
			m.clearChordState()
			if inQueueZone {
				m.clearFocusedBlock()
				if idx, remove, ok := m.queuedDraftActionAt(mouse.X, mouse.Y); ok {
					if remove {
						return m.deleteQueuedDraftAt(idx)
					}
					return m.editQueuedDraftAt(idx)
				}
				return nil
			}
			if inInfoPanel {
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
			if m.statusSessionContainsPoint(mouse.X, mouse.Y) {
				m.clearFocusedBlock()
				now := time.Now()
				const doubleClickThreshold = 400 * time.Millisecond
				const clickTolerance = 2
				if now.Sub(m.statusPathLastClickTime) <= doubleClickThreshold &&
					abs(mouse.X-m.statusPathLastClickX) <= clickTolerance &&
					abs(mouse.Y-m.statusPathLastClickY) <= clickTolerance {
					m.statusPathClickCount++
				} else {
					m.statusPathClickCount = 1
				}
				m.statusPathLastClickTime = now
				m.statusPathLastClickX = mouse.X
				m.statusPathLastClickY = mouse.Y
				m.clearMouseSelection()
				m.input.ClearSelection()
				m.inputMouseDown = false
				if m.statusPathClickCount >= 2 {
					m.statusPathClickCount = 0
					return writeStatusSessionClipboardCmd(m.statusSession.value)
				}
				return nil
			}
			if m.statusPathContainsPoint(mouse.X, mouse.Y) {
				m.clearFocusedBlock()
				now := time.Now()
				const doubleClickThreshold = 400 * time.Millisecond
				const clickTolerance = 2
				if now.Sub(m.statusPathLastClickTime) <= doubleClickThreshold &&
					abs(mouse.X-m.statusPathLastClickX) <= clickTolerance &&
					abs(mouse.Y-m.statusPathLastClickY) <= clickTolerance {
					m.statusPathClickCount++
				} else {
					m.statusPathClickCount = 1
				}
				m.statusPathLastClickTime = now
				m.statusPathLastClickX = mouse.X
				m.statusPathLastClickY = mouse.Y
				m.clearMouseSelection()
				m.input.ClearSelection()
				m.inputMouseDown = false
				if m.statusPathClickCount >= 2 {
					m.statusPathClickCount = 0
					return writeStatusPathClipboardCmd(m.statusPath.value)
				}
				return nil
			}
			// Insert mode: click outside input zone -> switch to Normal and blur,
			// then fall through to start selection so drag works immediately.
			if m.mode == ModeInsert {
				if !inInputZone {
					cmd := m.switchModeWithIME(ModeNormal)
					m.recalcViewportSize()
					if cmd != nil {
						return cmd
					}
					// Fall through to Normal-mode selection handling below.
				} else {
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
					return nil
				}
			}
			// Normal mode: click in input zone -> switch to Insert and focus.
			if m.mode == ModeNormal && inInputZone {
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
				return m.input.Focus()
			}
			if inViewport {
				m.input.ClearSelection()
				m.inputMouseDown = false
				if block, lineInBlock := m.viewportResolveMouse(viewportLine); block != nil {
					col := clampCol(viewportCol, m.viewport.width)
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
							m.mouseDown = false
						} else {
							m.mouseDown = true
							m.selStartBlockID = block.ID
							m.selStartLine = lineInBlock
							m.selStartCol = col
							m.selEndBlockID = block.ID
							m.selEndLine = lineInBlock
							m.selEndCol = col
						}
					} else if m.clickCount >= 3 {
						_, lineWidth := m.viewport.GetLinePlain(block.ID, lineInBlock)
						m.clickCount = 0
						if lineWidth > 0 {
							m.selStartBlockID = block.ID
							m.selStartLine = lineInBlock
							m.selStartCol = 0
							m.selEndBlockID = block.ID
							m.selEndLine = lineInBlock
							m.selEndCol = lineWidth
							m.mouseDown = false
						} else {
							// Empty line: treat as single click (point selection).
							m.mouseDown = true
							m.selStartBlockID = block.ID
							m.selStartLine = lineInBlock
							m.selStartCol = col
							m.selEndBlockID = block.ID
							m.selEndLine = lineInBlock
							m.selEndCol = col
						}
					} else {
						if block.ID != m.focusedBlockID {
							if m.focusedBlockID >= 0 {
								for _, b := range m.viewport.blocks {
									if b.ID == m.focusedBlockID {
										b.Focused = false
										b.InvalidateCache()
										break
									}
								}
							}
							if isSelectableBlockType(block.Type) {
								m.focusedBlockID = block.ID
								block.Focused = true
								block.InvalidateCache()
							} else {
								m.focusedBlockID = -1
							}
							// Clicking a Delegate block that has a linked subagent switches to that agent's view.
							m.maybeSwitchToTaskAgent(block)
							if part, ok := block.imagePartAtPoint(lineInBlock, col, m.viewport.width); ok {
								m.clearMouseSelection()
								m.mouseDown = false
								return openBlockImageCmd(m.runtimeImageOpenDir(), block, part, m.imageCaps)
							}
							// Do not scroll on click; only j/k/g/G etc. reposition the view.
						} else {
							// Already focused: clicking again on a Delegate block still switches to subagent view.
							m.maybeSwitchToTaskAgent(block)
							if part, ok := block.imagePartAtPoint(lineInBlock, col, m.viewport.width); ok {
								m.clearMouseSelection()
								m.mouseDown = false
								return openBlockImageCmd(m.runtimeImageOpenDir(), block, part, m.imageCaps)
							}
						}
						m.mouseDown = true
						m.selStartBlockID = block.ID
						m.selStartLine = lineInBlock
						m.selStartCol = col
						m.selEndBlockID = block.ID
						m.selEndLine = lineInBlock
						m.selEndCol = col
					}
				}
			} else {
				m.input.ClearSelection()
				m.inputMouseDown = false
				m.clearFocusedBlock()
				// Outside viewport: resolve focus from zones (e.g. directory overlay).
				// Zone bounds are from last Scan; we don't pass v2 MouseMsg to bubblezone.
				// Use viewport resolution for blocks (already have inViewport coords).
				for _, b := range m.viewport.blocks {
					idStr := fmt.Sprintf("block-%d", b.ID)
					z := m.zone.Get(idStr)
					if !z.IsZero() && mouse.X >= z.StartX && mouse.X <= z.EndX && mouse.Y >= z.StartY && mouse.Y <= z.EndY {
						if b.ID != m.focusedBlockID {
							if m.focusedBlockID >= 0 {
								for _, oldB := range m.viewport.blocks {
									if oldB.ID == m.focusedBlockID {
										oldB.Focused = false
										oldB.InvalidateCache()
										break
									}
								}
							}
							if isSelectableBlockType(b.Type) {
								m.focusedBlockID = b.ID
								b.Focused = true
								b.InvalidateCache()
							} else {
								m.focusedBlockID = -1
							}
							m.maybeSwitchToTaskAgent(b)
						}
						break
					}
				}
			}
			return nil
		}
	case tea.MouseMotionMsg:
		if m.inputMouseDown && inInputZone {
			if line, col, ok := m.input.SelectionPointAt(
				mouse.Y-m.layout.input.Min.Y-1,
				mouse.X-m.layout.input.Min.X-inputPromptWidth,
			); ok {
				m.input.UpdateSelection(line, col)
			}
			return nil
		}
		if m.mouseDown && inViewport {
			if block, lineInBlock := m.viewportResolveMouse(viewportLine); block != nil {
				m.selEndBlockID = block.ID
				m.selEndLine = lineInBlock
				m.selEndCol = clampCol(viewportCol, m.viewport.width)
			}
			return nil
		}
	case tea.MouseReleaseMsg:
		if mouse.Button == tea.MouseLeft {
			m.mouseDown = false
			m.inputMouseDown = false
		}
	}

	// Wheel: scroll viewport
	if _, isWheel := msg.(tea.MouseWheelMsg); isWheel {
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.pendingScrollDelta -= 3
			return m.scheduleScrollFlush(16 * time.Millisecond)
		case tea.MouseWheelDown:
			m.pendingScrollDelta += 3
			return m.scheduleScrollFlush(16 * time.Millisecond)
		}
	}

	return nil
}
