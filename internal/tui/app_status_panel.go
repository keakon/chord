package tui

import (
	"image"
	"strings"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"
)

type statusPanelState struct {
	prevMode Mode
	section  infoPanelSectionID
}

func (m *Model) openStatusPanel() tea.Cmd {
	if m.mode == ModeStatus {
		return nil
	}
	prev := m.mode
	m.clearActiveSearch()
	m.clearChordState()
	if prev == ModeInsert {
		m.input.Blur()
	}
	m.statusPanel = statusPanelState{prevMode: prev}
	m.mode = ModeStatus
	m.infoPanelScrollOffset = 0
	m.clearInfoPanelViewportCache()
	m.recalcViewportSize()
	return nil
}

func (m *Model) closeStatusPanel() tea.Cmd {
	prev := m.statusPanel.prevMode
	m.statusPanel = statusPanelState{}
	m.clearInfoPanelViewportCache()
	cmd := m.restoreModeWithIME(prev)
	m.recalcViewportSize()
	if prev == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) handleStatusPanelKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		return m.closeStatusPanel()
	case "j", "down":
		m.scrollInfoPanel(1)
	case "k", "up":
		m.scrollInfoPanel(-1)
	case "pgdown", "ctrl+f":
		m.scrollInfoPanel(max(m.infoPanelViewportHeight-1, 1))
	case "pgup", "ctrl+b":
		m.scrollInfoPanel(-max(m.infoPanelViewportHeight-1, 1))
	case "g":
		m.scrollInfoPanel(-m.infoPanelContentHeight)
	case "G":
		m.scrollInfoPanel(m.infoPanelContentHeight)
	case "tab", "shift+tab":
		m.cycleStatusPanelSection(msg.String() == "shift+tab")
	case "enter", "space":
		if m.statusPanel.section != "" {
			m.toggleInfoPanelSection(m.statusPanel.section)
		}
	}
	return nil
}

func (m *Model) cycleStatusPanelSection(backward bool) {
	headers := make([]infoPanelSectionHitBox, 0, len(m.infoPanelHitBoxes))
	current := -1
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == "" {
			continue
		}
		if hit.section == m.statusPanel.section {
			current = len(headers)
		}
		headers = append(headers, hit)
	}
	if len(headers) == 0 {
		return
	}
	next := (current + 1) % len(headers)
	if backward {
		next = (current - 1 + len(headers)) % len(headers)
		if current < 0 {
			next = len(headers) - 1
		}
	}
	hit := headers[next]
	m.statusPanel.section = hit.section
	m.infoPanelScrollOffset = max(min(hit.startY, max(m.infoPanelContentHeight-m.infoPanelViewportHeight, 0)), 0)
	m.clearInfoPanelViewportCache()
}

func (m *Model) drawStatusPanel(scr uv.Screen, layout tuiLayout) {
	body := m.renderInfoPanel(layout.infoPanel.Dx(), layout.infoPanel.Dy())
	m.renderOverlayCached(scr, layout.infoPanel, &m.cachedDirRender, body)
	hint := hintLine(hint("j/k", "scroll"), hint("Tab", "section"), hint("Enter", "fold"), hint("Esc", "close"))
	if m.infoPanelContentHeight > m.infoPanelViewportHeight {
		hint = appendHintText(hint, formatTokens(m.infoPanelScrollOffset+m.infoPanelViewportHeight)+"/"+formatTokens(m.infoPanelContentHeight))
	}
	rect := image.Rect(layout.main.Min.X, layout.infoPanel.Max.Y, layout.main.Max.X, layout.main.Max.Y)
	m.renderOverlayCached(scr, rect, &m.cachedStatusPanelHint, ansi.Truncate(hint, max(rect.Dx()-1, 1), "…"))
}

// highlightStatusSection adds a shape cue without modifying the complete panel
// content cache or the row ownership used by clicks.
func (m *Model) highlightStatusSection(lines []string, start int) []string {
	if m.mode != ModeStatus || m.statusPanel.section == "" {
		return lines
	}
	for _, hit := range m.infoPanelHitBoxes {
		row := hit.startY - start
		if hit.section == m.statusPanel.section && row >= 0 && row < len(lines) {
			lines = append([]string(nil), lines...)
			width := max(m.cachedInfoPanelW-2, 1)
			text := "▸ " + strings.TrimSpace(ansi.Strip(lines[row]))
			lines[row] = InfoPanelLineBg.Width(width).Render(InfoPanelValue.Render(ansi.Truncate(text, width, "…")))
			break
		}
	}
	return lines
}
