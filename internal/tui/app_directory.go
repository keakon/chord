package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// renderDirectory produces the Ctrl+J overlay that fills the viewport area.
func (m Model) renderDirectory() string {
	if len(m.dirEntries) == 0 {
		centred := lipgloss.Place(m.viewport.width, m.viewport.height,
			lipgloss.Center, lipgloss.Center, DimStyle.Render("(no messages)"))
		return centred
	}

	maxWidth := m.width - 6
	if maxWidth < 20 {
		maxWidth = 20
	}

	// innerWidth is the usable content width inside the DirectoryBorderStyle
	// box, which has Padding(0, 1) — 1 char on each side.
	innerWidth := maxWidth - 2
	if innerWidth < 16 {
		innerWidth = 16
	}
	if m.dirList == nil {
		return ""
	}
	m.dirList.SetMaxVisible(m.directoryMaxVisible())
	content := m.dirList.Render(innerWidth)
	title := DialogTitleStyle.Render("Message Directory")
	box := DirectoryBorderStyle.Width(maxWidth).Render(title + "\n" + content)

	return lipgloss.Place(m.width, m.viewport.height, lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) directoryMaxVisible() int {
	maxVisible := m.viewport.height - 4
	if maxVisible < 3 {
		maxVisible = 3
	}
	return maxVisible
}

func directoryItems(entries []DirectoryEntry) []OverlayListItem {
	items := make([]OverlayListItem, 0, len(entries))
	for _, entry := range entries {
		items = append(items, OverlayListItem{
			ID:    fmt.Sprintf("%d", entry.BlockIndex),
			Label: entry.Summary,
		})
	}
	return items
}
