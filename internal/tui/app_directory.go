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
	body := preserveDialogBackground(title + "\n" + content)
	box := DirectoryBorderStyle.Width(maxWidth).Render(body)

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

// currentDirectoryAnchorBlockID returns the block ID the message-directory
// cursor should start on: the focused block when one exists, otherwise the
// block at the viewport offset.
func (m *Model) currentDirectoryAnchorBlockID() int {
	return m.currentBlockID()
}

// directoryCursorForBlockID returns the entry index whose BlockID matches the
// current card, or 0 when no entry matches. Matching must go by block ID, not
// LineOffset: in the deferred transcript path directory LineOffsets are
// full-transcript coordinates while the viewport offset is window-relative.
func directoryCursorForBlockID(entries []DirectoryEntry, blockID int) int {
	if blockID < 0 {
		return 0
	}
	for i, entry := range entries {
		if entry.BlockID == blockID {
			return i
		}
	}
	return 0
}
