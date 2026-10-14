package tui

import (
	tea "github.com/keakon/bubbletea/v2"
)

var fileActions = []string{"Preview", "Open", "Open with", "Copy path"}

func (m *Model) activateFileAction(index int) tea.Cmd {
	switch index {
	case 0:
		return m.prepareFileViewer()
	case 1:
		return m.fileDesktopAction("open", "")
	case 2:
		return m.fileDesktopAction("associations", "")
	case 3:
		return writeClipboardCmd(deliveryPath(m.fileViewer.files[m.fileViewer.selected], m.deliverySessionDir()), "File path copied")
	}
	return nil
}
func fileActionRows(width, selected int) ([]string, [][3]int) {
	rows := []string{""}
	var hits [][3]int
	for i, label := range fileActions {
		button := "[" + label + "]"
		if i == selected {
			button = ">" + button
		}
		if len(rows[len(rows)-1])+len(button)+1 > width && rows[len(rows)-1] != "" {
			rows = append(rows, "")
		}
		row := len(rows) - 1
		start := len(rows[row])
		rows[row] += button + " "
		hits = append(hits, [3]int{row, start, start + len(button)})
	}
	return rows, hits
}
func (m *Model) handleFileViewerMouse(msg tea.MouseMsg) tea.Cmd {
	mouse := msg.Mouse()
	if mouse.Button == tea.MouseWheelUp {
		m.scrollFileViewer(-3)
		return nil
	}
	if mouse.Button == tea.MouseWheelDown {
		m.scrollFileViewer(3)
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || mouse.Button != tea.MouseLeft {
		return nil
	}
	layout := m.fileViewerLayout()
	rect := centeredRect(m.ensureLayout().area, layout.render())
	x := mouse.X - rect.Min.X - currentStyles.DirectoryBorderStyle.GetBorderLeftSize() - currentStyles.DirectoryBorderStyle.GetPaddingLeft()
	y := mouse.Y - rect.Min.Y - currentStyles.DirectoryBorderStyle.GetBorderTopSize() - currentStyles.DirectoryBorderStyle.GetPaddingTop()
	for _, hit := range layout.hits {
		if y != hit.row || x < hit.start || x >= hit.end {
			continue
		}
		switch hit.kind {
		case fileHitAction:
			m.fileViewer.action = hit.index
			return m.activateFileAction(hit.index)
		case fileHitApplication:
			if hit.index >= 0 && hit.index < len(m.fileViewer.apps) {
				m.fileViewer.appIndex = hit.index
				return m.fileDesktopAction("with", m.fileViewer.apps[hit.index].ID)
			}
		case fileHitEntry:
			m.selectFileViewerFile(hit.index)
		}
		return nil
	}
	return nil
}
