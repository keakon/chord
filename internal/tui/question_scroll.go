package tui

func (m *Model) scrollQuestion(delta int) {
	m.question.followCursor = false
	m.question.scrollOffset = max(0, min(m.question.scrollOffset+delta, m.question.bodyHeight-m.question.visibleBodyHeight))
	m.question.renderCacheText = ""
}
