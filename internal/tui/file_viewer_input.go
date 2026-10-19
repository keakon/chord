package tui

import (
	tea "github.com/keakon/bubbletea/v2"
)

// fileActions is the canonical action set. The delivery card lists all four for
// every file, and the file viewer lists the ones that do something for its
// current state; both index into this slice, so the same action runs whichever
// surface the reader used.
var fileActions = []string{"Preview", "Open", "Open with", "Copy path"}

const (
	fileActionPreview = iota
	fileActionOpen
	fileActionOpenWith
	fileActionCopyPath
)

// fileActionKeys are the direct keys that run fileActions in the file viewer.
// Preview belongs to the list's Enter: once content is showing it would only
// reload the file already on screen, so the viewer drops that button.
var fileActionKeys = []string{"Enter", "o", "a", "p"}

func (m *Model) activateFileAction(index int) tea.Cmd {
	switch index {
	case fileActionPreview:
		return m.prepareFileViewer()
	case fileActionOpen:
		return m.fileDesktopAction("open", "")
	case fileActionOpenWith:
		return m.fileDesktopAction("associations", "")
	case fileActionCopyPath:
		return writeClipboardCmd(deliveryPath(m.fileViewer.files[m.fileViewer.selected], m.deliverySessionDir()), "File path copied")
	}
	return nil
}

// viewerActionButtons returns the file viewer's footer buttons for the current
// state, paired with the fileActions index each one runs.
func viewerActionButtons(showing bool) (buttons []string, indexes []int) {
	for i, label := range fileActions {
		if showing && i == fileActionPreview {
			continue
		}
		buttons = append(buttons, "["+fileActionKeys[i]+" "+label+"]")
		indexes = append(indexes, i)
	}
	return buttons, indexes
}

// deliveryActionButtons returns the delivery card's per-file buttons.
func deliveryActionButtons() []string {
	buttons := make([]string, len(fileActions))
	for i, label := range fileActions {
		buttons[i] = "[" + label + "]"
	}
	return buttons
}

// wrapActionButtons lays bracketed buttons out on rows that fit width and
// returns each button's [row, start, end] cell range, in the same order.
func wrapActionButtons(buttons []string, width int) ([]string, [][3]int) {
	rows := []string{""}
	var hits [][3]int
	for _, button := range buttons {
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
