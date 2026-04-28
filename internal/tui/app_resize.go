package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	immediateWidthShrinkCols  = 6
	immediateHeightShrinkRows = 4
)

func (m *Model) applyTerminalSize(width, height int, refreshKitty bool) {
	m.recordTUIDiagnostic("apply-terminal-size", "from=%dx%d to=%dx%d refresh_kitty=%t", m.width, m.height, width, height, refreshKitty)
	m.width = width
	m.height = height
	m.stableWidth = width
	m.stableHeight = height
	m.updateRightPanelVisible()
	m.input.SetWidth(m.width - 2)
	if m.confirm.editing {
		ei := m.confirm.editInput
		ei.SetWidth(confirmDialogInnerWidth(m.width))
		ei.SetHeight(confirmEditHeight(m.height))
		m.confirm.editInput = ei
	}
	if m.mode == ModeQuestion {
		qin := m.question.input
		qin.SetWidth(questionInputWidth(m.width) + 2)
		m.question.input = qin
	}
	if m.mode == ModeSearch {
		sr := m.search.Input
		sr.SetWidth(m.width - 4)
		m.search.Input = sr
	}
	m.imageViewer.RenderGen++
	if refreshKitty {
		m.refreshKittyTerminalMetrics()
	}
	m.resetKittyPlacements()
	m.input.syncHeight()
	m.recalcViewportSize()
}

func (m *Model) restoreStableTerminalSize() {
	m.recordTUIDiagnostic("restore-stable-size", "stable=%dx%d current=%dx%d", m.stableWidth, m.stableHeight, m.width, m.height)
	if m.stableWidth <= 0 || m.stableHeight <= 0 {
		return
	}
	if m.width == m.stableWidth && m.height == m.stableHeight {
		return
	}
	m.width = m.stableWidth
	m.height = m.stableHeight
	m.updateRightPanelVisible()
	m.input.SetWidth(m.width - 2)
	if m.confirm.editing {
		ei := m.confirm.editInput
		ei.SetWidth(confirmDialogInnerWidth(m.width))
		ei.SetHeight(confirmEditHeight(m.height))
		m.confirm.editInput = ei
	}
	if m.mode == ModeQuestion {
		qin := m.question.input
		qin.SetWidth(questionInputWidth(m.width) + 2)
		m.question.input = qin
	}
	if m.mode == ModeSearch {
		sr := m.search.Input
		sr.SetWidth(m.width - 4)
		m.search.Input = sr
	}
	m.imageViewer.RenderGen++
	m.resetKittyPlacements()
	m.input.syncHeight()
	m.recalcViewportSize()
}

func focusResizeSettleCmd(generation int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return focusResizeSettleMsg{generation: generation}
	})
}

func imageProtocolReplayCmd(generation int, reason string, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg {
		return imageProtocolTickMsg{generation: generation, reason: reason}
	})
}
