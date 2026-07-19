package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// renderDirectory produces the Ctrl+T message directory within the main viewport area.
func (m Model) renderDirectory() string {
	width := m.viewport.width
	height := m.viewport.height
	if width <= 0 {
		width = m.width
	}
	if height <= 0 {
		height = 1
	}
	if len(m.dirEntries) == 0 {
		centred := lipgloss.Place(width, height,
			lipgloss.Center, lipgloss.Center, DimStyle.Render("(no messages)"))
		return centred
	}

	maxWidth := min(max(width-6, 20), width)

	// innerWidth is the usable content width inside the DirectoryBorderStyle
	// box, which has Padding(0, 1) — 1 char on each side.
	innerWidth := max(maxWidth-2, 16)
	if m.dirList == nil {
		return ""
	}
	m.dirList.SetMaxVisible(m.directoryMaxVisible())
	content := m.dirList.Render(innerWidth)
	title := DialogTitleStyle.Render("Message Directory")
	box := DirectoryBorderStyle.Width(maxWidth).Render(title + "\n" + content)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func (m *Model) directoryMaxVisible() int {
	maxVisible := max(m.viewport.height-4, 3)
	return maxVisible
}

func directoryItems(entries []DirectoryEntry) []OverlayListItem {
	items := make([]OverlayListItem, 0, len(entries))
	for i, entry := range entries {
		items = append(items, OverlayListItem{
			ID:    fmt.Sprintf("%d", entry.BlockIndex),
			Label: fmt.Sprintf("%d. %s", i+1, entry.Summary),
		})
	}
	return items
}
