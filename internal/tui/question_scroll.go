package tui

import "image"

// questionScrollTarget clamps a scroll delta to the question body's range.
func (m *Model) questionScrollTarget(delta int) int {
	return max(0, min(m.question.scrollOffset+delta, m.question.bodyHeight-m.question.visibleBodyHeight))
}

// scrollQuestion moves the question body by delta lines and reports whether it
// actually moved. At an edge (or when the body fits) it leaves the input for
// the caller to spend on the conversation behind the dialog.
func (m *Model) scrollQuestion(delta int) bool {
	next := m.questionScrollTarget(delta)
	if next == m.question.scrollOffset {
		return false
	}
	m.question.followCursor = false
	m.question.scrollOffset = next
	m.question.renderCacheText = ""
	return true
}

// questionWheelScrollsBody reports whether a wheel event at (x, y) should scroll
// the question body: the pointer must be over the dialog and the body must have
// room in that direction. Otherwise the wheel reads the conversation behind the
// dialog, whose context the question often refers to.
func (m *Model) questionWheelScrollsBody(x, y, delta int) bool {
	if !image.Pt(x, y).In(m.question.dialogRect) {
		return false
	}
	return m.questionScrollTarget(delta) != m.question.scrollOffset
}
