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
		configureDialogTextarea(&ei, confirmDialogInnerWidth(m.width), confirmEditMinHeight, confirmEditHeight(m.height))
		m.confirm.editInput = ei
	}
	if m.confirm.denyingWithReason {
		dri := m.confirm.denyReasonInput
		configureDialogTextarea(&dri, confirmDialogInnerWidth(m.width), confirmEditMinHeight, confirmEditHeight(m.height))
		m.confirm.denyReasonInput = dri
	}
	if m.mode == ModeQuestion {
		qin := m.question.input
		configureDialogTextarea(&qin, questionInputWidth(m.width), 1, questionInputHeight)
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
