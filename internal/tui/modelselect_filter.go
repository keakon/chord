package tui

import (
	"strings"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	modelSelectIdleHint   = "j/k move  enter select  / filter  esc close"
	modelSelectFilterHint = "type to filter  enter select  esc clear"
	selectorFilterHint    = "type to filter  enter keep  esc clear"
)

func (s *modelSelectState) filteredItems() []OverlayListItem {
	tokens := strings.Fields(strings.ToLower(s.filter))
	items := buildModelSelectItems(s.poolNames, s.currentPool)
	filtered := items[:0]
	for _, item := range items {
		matches := true
		for _, token := range tokens {
			if !strings.Contains(strings.ToLower(item.ID), token) {
				matches = false
				break
			}
		}
		if matches {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (m *Model) rebuildModelSelectFilter() {
	m.modelSelect.selector.ensureList(m.modelSelectMaxVisible())
	m.modelSelect.selector.list.SetItems(m.modelSelect.filteredItems())
	m.modelSelect.selector.list.CursorToTop()
	m.modelSelect.poolCursor = m.modelSelect.selector.list.CursorAt()
}

func (m *Model) handleModelSelectFilterKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.modelSelect.filter = ""
		m.modelSelect.filterFocused = false
	case "enter":
		m.modelSelect.filterFocused = false
		return m.selectPoolAtCursor()
	case "ctrl+u":
		m.modelSelect.filter = ""
	case "backspace":
		runes := []rune(m.modelSelect.filter)
		if len(runes) > 0 {
			m.modelSelect.filter = string(runes[:len(runes)-1])
		}
	case "up", "down":
		if list := m.modelSelect.selector.list; list != nil {
			if msg.String() == "up" {
				list.CursorUp()
			} else {
				list.CursorDown()
			}
			m.modelSelect.poolCursor = list.CursorAt()
		}
		return nil
	default:
		if msg.Key().Code == tea.KeySpace {
			m.modelSelect.filter += " "
		} else if text := msg.Key().Text; text != "" {
			m.modelSelect.filter += text
		} else {
			return nil
		}
	}
	m.rebuildModelSelectFilter()
	return nil
}
