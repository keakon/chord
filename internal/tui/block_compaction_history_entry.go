package tui

import (
	"fmt"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/message"
)

// A compaction checkpoint is the only card that names the archived history it
// replaced. Its "view referenced history" entry is the door into that
// history: the entry is drawn only when the checkpoint really lists an
// archive, and it opens the archives the checkpoint named — not the newest
// snapshot of the session.

// compactionCardEntryHit records where the "view referenced history" entry of
// a compaction summary card sits inside the card. It is rebuilt whenever the
// card renders, so a theme that changes the card geometry cannot leave a
// stale hit behind.
type compactionCardEntryHit struct {
	line, start, end int
}

// compactionCardEntryLabel names the entry and how many archives it opens.
func compactionCardEntryLabel(count int) string {
	if count == 1 {
		return "View referenced history · 1 archive"
	}
	return fmt.Sprintf("View referenced history · %d archives", count)
}

// compactionSummaryRaw returns the persisted checkpoint text of a compaction
// summary card. The raw copy survives a spill, so the entry keeps working
// after the card was moved out of memory.
func (b *Block) compactionSummaryRaw() string {
	if b == nil {
		return ""
	}
	if b.CompactionSummaryRaw != "" {
		return b.CompactionSummaryRaw
	}
	return b.Content
}

// compactionSummaryArchiveRefs returns the archives the checkpoint named.
func (b *Block) compactionSummaryArchiveRefs() []message.CompactionArchiveRef {
	if b == nil {
		return nil
	}
	return message.ParseCompactionArchiveRefs(b.compactionSummaryRaw())
}

// compactionSummaryCardEntryOrigin returns where the card body starts in
// viewport coordinates: the row and the column of its first cell. It derives
// both from the same style the card frame uses, so the entry stays clickable
// when a theme changes the card geometry.
func compactionSummaryCardEntryOrigin(style lipgloss.Style) (row, col int) {
	rail := 0
	if cardRailPrefix("assistant", false) != "" {
		rail = cardRailWidth
	}
	return style.GetMarginTop() + style.GetPaddingTop(),
		rail + style.GetMarginLeft() + style.GetPaddingLeft()
}

// activateCompactionSummaryHistory opens the history the checkpoint named. It
// reports whether the click hit the entry; a miss falls through to the normal
// click handling, which starts a text selection.
func (m *Model) activateCompactionSummaryHistory(block *Block, line, column int) (tea.Cmd, bool) {
	hit := block.compactionEntry
	if hit == nil || line != hit.line || column < hit.start || column >= hit.end {
		return nil, false
	}
	refs := block.compactionSummaryArchiveRefs()
	if len(refs) == 0 {
		return nil, false
	}
	return m.openCompactionHistoryWithSources(refs), true
}

// openCompactionSummaryHistory opens the history a compaction summary card
// named. It is the keyboard route into the same entry the card draws.
func (m *Model) openCompactionSummaryHistory(block *Block) tea.Cmd {
	if block == nil {
		return nil
	}
	refs := block.compactionSummaryArchiveRefs()
	if len(refs) == 0 {
		return nil
	}
	return m.openCompactionHistoryWithSources(refs)
}
