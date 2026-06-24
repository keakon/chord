package tui

const (
	immediateWidthShrinkCols  = 6
	immediateHeightShrinkRows = 4
)

func (m *Model) applyTerminalSize(width, height int, refreshKitty bool) {
	m.recordTUIDiagnostic("apply-terminal-size", "from=%dx%d to=%dx%d refresh_kitty=%t", m.width, m.height, width, height, refreshKitty)
	m.width = width
	m.height = height
	m.updateRightPanelVisible()
	m.input.SetWidth(m.width - 2)
	if m.confirm.editing {
		ei := m.confirm.editInput
		ei.SetWidth(confirmDialogInnerWidth(m.width))
		ei.SetHeight(confirmEditHeight(m.height))
		m.confirm.editInput = ei
	}
	if m.confirm.denyingWithReason {
		dri := m.confirm.denyReasonInput
		dri.SetWidth(confirmDialogInnerWidth(m.width))
		dri.SetHeight(confirmEditHeight(m.height))
		m.confirm.denyReasonInput = dri
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
