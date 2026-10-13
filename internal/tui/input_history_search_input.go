package tui

import (
	"image"
	"unicode/utf8"

	"github.com/keakon/chord/internal/inputhistory"

	tea "github.com/keakon/bubbletea/v2"
)

func (m *Model) moveInputHistorySelection(delta int) {
	s := &m.inputHistory
	next := min(max(0, s.selected+delta), max(0, len(s.results.Hits)-1))
	if next != s.selected {
		s.selected = next
		m.prepareInputHistorySelection()
	}
}
func (m *Model) scrollInputHistoryPreview(back bool) {
	s := &m.inputHistory
	if back {
		if len(s.previewBack) == 0 {
			if s.previewOffset > 0 {
				s.previewDirection = -1
				m.queueInputHistoryPreview()
			}
			return
		}
		last := len(s.previewBack) - 1
		s.previewOffset = s.previewBack[last]
		s.previewBack = s.previewBack[:last]
	} else {
		if !s.preview.HasAfter {
			return
		}
		if len(s.previewBack) >= 256 {
			s.previewBack = append([]int(nil), s.previewBack[1:]...)
		}
		lastCheckpoint := 0
		if len(s.previewCheckpoints) > 0 {
			lastCheckpoint = s.previewCheckpoints[len(s.previewCheckpoints)-1]
		}
		if s.preview.Start-lastCheckpoint >= 16<<10 {
			s.previewCheckpoints = append(s.previewCheckpoints, s.preview.Start)
		}
		s.previewBack = append(s.previewBack, s.preview.Start)
		s.previewOffset = s.preview.End
	}
	m.queueInputHistoryPreview()
}
func (m *Model) handleInputHistoryKey(msg tea.KeyMsg) tea.Cmd {
	if m.mode == ModeInputHistoryClearConfirm {
		return m.handleInputHistoryClearKey(msg)
	}
	s := &m.inputHistory
	switch msg.String() {
	case "esc", "ctrl+c":
		m.closeInputHistory()
		return nil
	case "enter":
		return m.applyInputHistorySelection()
	case "up":
		m.moveInputHistorySelection(-1)
		return nil
	case "down", "ctrl+r":
		m.moveInputHistorySelection(1)
		return nil
	case "pgup":
		m.moveInputHistorySelection(-10)
		return nil
	case "pgdown":
		m.moveInputHistorySelection(10)
		return nil
	case "ctrl+pgup":
		m.scrollInputHistoryPreview(true)
		return nil
	case "ctrl+pgdown":
		m.scrollInputHistoryPreview(false)
		return nil
	case "tab", "shift+tab":
		step := 1
		if msg.String() == "shift+tab" {
			step = 2
		}
		s.focus = (s.focus + step) % 3
		if s.focus == 0 {
			s.query.Focus()
		} else {
			s.query.Blur()
		}
		return nil
	case "ctrl+u":
		s.query.SetValue("")
		s.queryLimited = false
		m.queueInputHistoryMatch(true)
		return nil
	case "space", "left", "right":
		if s.focus > 0 {
			if s.focus == 1 {
				s.allProjects = !s.allProjects
			} else {
				s.allKinds = !s.allKinds
			}
			m.queueInputHistoryMatch(true)
			return nil
		}
	}
	if s.focus != 0 {
		return nil
	}
	before := s.query.Value()
	var cmd tea.Cmd
	s.query, cmd = s.query.Update(msg)
	value := s.query.Value()
	normalized := inputhistory.NormalizeQuery(value)
	s.queryLimited = len(normalized) < len(value)
	s.query.SetValue(normalized)
	if before != s.query.Value() {
		m.queueInputHistoryMatch(true)
	}
	return cmd
}
func (m *Model) pasteInputHistoryQuery(value string) tea.Cmd {
	if m.mode != ModeInputHistorySearch {
		return nil
	}
	s := &m.inputHistory
	clipped := inputhistory.NormalizeQuery(value)
	before := []rune(s.query.Value())
	pos := min(s.query.Position(), len(before))
	combined := string(before[:pos]) + clipped + string(before[pos:])
	normalized := inputhistory.NormalizeQuery(combined)
	s.queryLimited = len(clipped) < len(value) || len(normalized) < len(combined)
	s.query.SetValue(normalized)
	s.query.SetCursor(min(pos+utf8.RuneCountInString(clipped), utf8.RuneCountInString(normalized)))
	m.queueInputHistoryMatch(true)
	if s.queryLimited {
		return m.enqueueToast("History query limited to 256 characters and 16 words", "info")
	}
	return nil
}
func (m *Model) handleInputHistoryMouse(msg tea.MouseMsg) tea.Cmd {
	if m.mode != ModeInputHistorySearch {
		return nil
	}
	s := &m.inputHistory
	mouse := msg.Mouse()
	point := image.Pt(mouse.X, mouse.Y)
	if mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown {
		back := mouse.Button == tea.MouseWheelUp
		if point.In(s.previewRect) {
			m.scrollInputHistoryPreview(back)
		} else if point.In(s.listRect) {
			delta := 3
			if back {
				delta = -3
			}
			m.moveInputHistorySelection(delta)
		}
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); !ok || mouse.Button != tea.MouseLeft {
		return nil
	}
	for i, rect := range s.filterRects {
		if point.In(rect) {
			s.focus = i + 1
			s.query.Blur()
			if i == 0 {
				s.allProjects = !s.allProjects
			} else {
				s.allKinds = !s.allKinds
			}
			m.queueInputHistoryMatch(true)
			return nil
		}
	}
	if point.In(s.listRect) {
		m.moveInputHistorySelection(s.listStart + mouse.Y - s.listBase - s.selected)
	}
	return nil
}

type inputHistoryClipboardMsg struct {
	generation uint64
	text       string
	err        error
}

func (m *Model) pasteInputHistoryClipboard() tea.Cmd {
	g := m.inputHistory.token.generation
	return func() tea.Msg { text, err := clipboardReadAll(); return inputHistoryClipboardMsg{g, text, err} }
}
