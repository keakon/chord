package tui

import (
	"image"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/memory"
)

func (m *Model) handleMemoryPanelKey(msg tea.KeyMsg) tea.Cmd {
	p := &m.memoryPanel
	key := msg.String()
	if key == "ctrl+m" {
		msg = tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
		key = "enter"
	}
	if p.inputFocused || p.instructionMode {
		return m.handleMemoryInputKey(msg)
	}
	if key == "esc" || key == "q" {
		if p.detail {
			p.detail = false
			p.preview = nil
			m.contentViewer = contentViewerState{}
			m.clearChordState()
			return nil
		}
		return m.closeMemoryPanel()
	}
	if p.loading {
		return nil
	}
	if p.detail {
		m.contentViewer = p.viewer
		defer func() { p.viewer = m.contentViewer }()
		if key == "a" && p.preview != nil {
			return m.applyMemoryChange(false)
		}
		if key == "p" && p.preview == nil {
			return m.copyMemoryPath()
		}
		return m.handleMemoryDetailKey(msg)
	}
	switch key {
	case "/":
		p.inputFocused = true
		m.memoryPanelInputIME()
		return nil
	case "tab":
		p.tab = (p.tab + 1) % 3
		p.query = ""
		p.list = nil
		m.rebuildMemoryList()
	case "j", "down":
		if p.list != nil {
			p.list.CursorDown()
		}
	case "k", "up":
		if p.list != nil {
			p.list.CursorUp()
		}
	case "g", "home":
		if p.list != nil {
			p.list.CursorToTop()
		}
	case "G", "end":
		if p.list != nil {
			p.list.CursorToBottom()
		}
	case "ctrl+f", "pgdown":
		if p.list != nil {
			p.list.CursorPageDown()
		}
	case "ctrl+b", "pgup":
		if p.list != nil {
			p.list.CursorPageUp()
		}
	case "enter":
		title, content, _ := m.memoryCurrentContent()
		if content != "" {
			m.showMemoryDetail(title, content)
		}
	case "space":
		if m.memoryPanelCursorIsRecord() {
			item, _ := p.list.SelectedItem()
			if p.selected[item.ID] {
				delete(p.selected, item.ID)
			} else {
				p.selected[item.ID] = true
			}
			m.rebuildMemoryList()
		}
	case "y", "Y":
		return m.copyMemoryContent()
	case "p":
		return m.copyMemoryPath()
	case "d":
		if p.tab != 0 {
			return m.enqueueToast("Switch to project memories to remove", "info")
		}
		base, err := m.memorySelectedSnapshot(false)
		if err != nil {
			return m.enqueueToast(err.Error(), "info")
		}
		p.preview = memory.NewRemovalDraft(base)
		m.showMemoryDetail("Remove memory — a confirm, Esc cancel", p.preview.Preview())
	case "o", "O":
		if m.memoryMainBusy() {
			return m.enqueueToast("Wait until the main agent is idle before organizing memory", "info")
		}
		if p.tab != 0 {
			return m.enqueueToast("Switch to project memories to organize", "info")
		}
		p.instructionMode = true
		p.instructionAll = key == "O"
		p.instruction = ""
		m.memoryPanelInputIME()
	case "u":
		if p.tab != 0 {
			return m.enqueueToast("Switch to project memories to undo", "info")
		}
		if p.view == nil || !p.view.Snapshot.CanUndo {
			return m.enqueueToast("Nothing to undo", "info")
		}
		return m.applyMemoryChange(true)
	case "r":
		c, _ := m.memoryController()
		p.seq++
		p.loading = true
		return m.loadMemoryPanel(c)
	}
	return nil
}

func (m *Model) handleMemoryInputKey(msg tea.KeyMsg) tea.Cmd {
	p := &m.memoryPanel
	key := msg.String()
	text := &p.query
	if p.instructionMode {
		text = &p.instruction
	}
	switch key {
	case "esc":
		*text = ""
		p.selected = map[string]bool{}
		p.inputFocused = false
		p.instructionMode = false
		m.rebuildMemoryList()
		m.memoryPanelInputIME()
		return nil
	case "enter":
		if p.instructionMode {
			return m.startMemoryOrganization(p.instructionAll, p.instruction)
		}
		p.inputFocused = false
		m.memoryPanelInputIME()
		return nil
	case "ctrl+u":
		*text = ""
	case "backspace":
		r := []rune(*text)
		if len(r) > 0 {
			*text = string(r[:len(r)-1])
		}
	case "up":
		if p.list != nil {
			p.list.CursorUp()
		}
		return nil
	case "down":
		if p.list != nil {
			p.list.CursorDown()
		}
		return nil
	default:
		m.appendMemoryInput(msg.Key().Text)
		return nil
	}
	if !p.instructionMode {
		p.selected = map[string]bool{}
		m.rebuildMemoryList()
	}
	return nil
}

func (m *Model) appendMemoryInput(value string) {
	p := &m.memoryPanel
	text := &p.query
	if p.instructionMode {
		text = &p.instruction
	}
	for _, r := range value {
		if len(*text)+len(string(r)) > 2000 {
			break
		}
		*text += string(r)
	}
	if !p.instructionMode {
		p.selected = map[string]bool{}
		m.rebuildMemoryList()
	}
}

func (m *Model) handleMemoryDetailKey(msg tea.KeyMsg) tea.Cmd {
	lines := m.memoryDetailLines()
	height := overlayContentHeight(m.memoryPanelOverlayConfig(), image.Rect(0, 0, m.width, m.height))
	limit := max(0, len(lines)-height)
	switch msg.String() {
	case "j", "down":
		m.contentViewer.scrollOffset++
	case "k", "up":
		m.contentViewer.scrollOffset--
	case "ctrl+f", "pgdown":
		m.contentViewer.scrollOffset += max(1, height-1)
	case "ctrl+b", "pgup":
		m.contentViewer.scrollOffset -= max(1, height-1)
	case "g", "home":
		m.contentViewer.scrollOffset = 0
	case "G", "end":
		m.contentViewer.scrollOffset = limit
	case "y", "Y":
		return m.copyMemoryText(m.contentViewer.content)
	}
	m.contentViewer.scrollOffset = min(max(0, m.contentViewer.scrollOffset), limit)
	return nil
}

// clickMemoryPanelList maps a click in the list to a row: the first click moves
// the cursor, a click on the active row opens it. Wheel handling stays in the
// mouse dispatcher.
func (m *Model) clickMemoryPanelList(x, y int) {
	p := &m.memoryPanel
	if p.list == nil || p.list.Len() == 0 {
		return
	}
	dialog := m.renderMemoryPanel()
	rect := m.overlayRect(dialog)
	if x < rect.Min.X || x >= rect.Max.X || y < rect.Min.Y || y >= rect.Max.Y {
		return
	}
	start, end := p.list.WindowRange()
	idx, ok := overlayItemIndexAt(rect, y, p.listBaseRow, start, end-start)
	if !ok {
		return
	}
	wasActive := p.list.CursorAt() == idx
	if !p.list.HandleClick(idx-start) || !wasActive {
		return
	}
	if title, content, _ := m.memoryCurrentContent(); content != "" {
		m.showMemoryDetail(title, content)
	}
}

func (m *Model) memoryPanelInputIME() {
	if m.memoryPanel.inputFocused || m.memoryPanel.instructionMode {
		m.runIMERestoreIfNeeded()
	} else {
		m.reapplyIMEForCurrentModeOnFocus()
	}
}
