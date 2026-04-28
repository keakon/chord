package tui

func (m *Model) infoPanelContainsPoint(x, y int) bool {
	return m.layout.infoPanel.Dx() > 0 &&
		x >= m.layout.infoPanel.Min.X && x < m.layout.infoPanel.Max.X &&
		y >= m.layout.infoPanel.Min.Y && y < m.layout.infoPanel.Max.Y
}
