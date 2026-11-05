package tui

import (
	"fmt"
	"strings"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// The source list is what a compaction checkpoint's card opens when it names
// more than one archive, or when the archive it names has no local snapshot
// left. It belongs to the read-only browsing layer: it never asks a question,
// never queues a request and never answers one.
//
// The list is deliberately explicit about what is missing. A checkpoint that
// names two archives but only has one of them locally still shows both, so the
// reader can see that the checkpoint referred to more than one source and that
// this session can only open one of them.

const (
	// compactionHistorySourceHeader introduces the source list.
	compactionHistorySourceHeader = "Sources named by this checkpoint"
	// compactionHistorySourceAvailable marks a source this session can open.
	compactionHistorySourceAvailable = "available"
	// compactionHistorySourceMissing marks a source without a local snapshot.
	compactionHistorySourceMissing = "no local snapshot"
	// compactionHistorySourceHint tells the reader how to pick a source.
	compactionHistorySourceHint = "↑/↓ to choose · Enter to open · Esc to go back"
)

// compactionHistorySource is one archive a checkpoint named, resolved against
// the local session directory.
type compactionHistorySource struct {
	ref message.CompactionArchiveRef
	// available is true when this session holds an applied snapshot with the
	// same archive number.
	available bool
	// position is the index of that snapshot in the snapshot list, or -1 when
	// the source has no local snapshot.
	position int
}

// resolveCompactionHistorySources matches the references of the checkpoint that
// opened the layer against the snapshots of this session.
//
// A single verified reference opens directly: there is nothing to choose.
// Everything else — several references, or a reference without a local
// snapshot — shows the source list, so the reader sees which sources the
// checkpoint named and which of them this session can still open. The list
// never falls back to the newest snapshot: a checkpoint that points at an
// archive this session cannot read must not appear to point somewhere else.
func (m *Model) resolveCompactionHistorySources() tea.Cmd {
	h := &m.compactionHistory
	byIndex := make(map[int]int, len(h.snapshots))
	for i, snapshot := range h.snapshots {
		byIndex[snapshot.Index] = i
	}
	h.sources = make([]compactionHistorySource, 0, len(h.pendingSources))
	available := 0
	for _, ref := range h.pendingSources {
		source := compactionHistorySource{ref: ref, position: -1}
		if position, ok := byIndex[ref.Index]; ok {
			source.available = true
			source.position = position
			available++
		}
		h.sources = append(h.sources, source)
	}
	h.pendingSources = nil
	h.sourceCursor = 0
	h.sourceListActive = true
	if len(h.sources) == 1 && available == 1 {
		h.sourceListActive = false
		return m.openCompactionHistorySource(0)
	}
	if available == 0 {
		h.notice = "None of the referenced archives has a local snapshot in this session"
	}
	m.invalidateDrawCaches()
	return nil
}

// openCompactionHistorySource opens the snapshot of the selected source.
func (m *Model) openCompactionHistorySource(cursor int) tea.Cmd {
	h := &m.compactionHistory
	if cursor < 0 || cursor >= len(h.sources) {
		return nil
	}
	source := h.sources[cursor]
	if !source.available {
		h.notice = fmt.Sprintf("history-%d.md has no local snapshot in this session", source.ref.Index)
		m.invalidateDrawCaches()
		return nil
	}
	h.sourceListActive = false
	h.notice = ""
	h.referencedPosition = source.position
	return m.requestCompactionHistorySnapshot(source.position)
}

// compactionHistorySourceLabel names one source row.
func compactionHistorySourceLabel(source compactionHistorySource) string {
	label := fmt.Sprintf("history-%d.md", source.ref.Index)
	if source.ref.Topics != "" {
		label += " · " + source.ref.Topics
	}
	return label
}

// renderCompactionHistorySources renders the source list of the checkpoint
// that opened the layer.
func (m *Model) renderCompactionHistorySources() string {
	h := &m.compactionHistory
	width := m.compactionHistoryViewportWidth()
	start, end, header := m.compactionHistorySourceWindow()
	rows := make([]string, 0, end-start+4)
	if header > 0 {
		rows = append(rows, currentStyles.DimStyle.Render(compactionHistorySourceHeader+":"))
		rows = append(rows, "")
	}
	for i := start; i < end; i++ {
		rows = append(rows, m.renderCompactionHistorySourceRow(i, h.sources[i], width))
	}
	rows = append(rows, "")
	rows = append(rows, currentStyles.DimStyle.Render(terminaltext.Truncate(compactionHistorySourceHint, width)))
	return strings.Join(rows, "\n")
}

func (m *Model) compactionHistorySourceWindow() (start, end, header int) {
	h := &m.compactionHistory
	height := m.compactionHistoryContentHeight()
	if height > 2 {
		header = 2
	}
	rows := max(height-header, 1)
	start = max(h.sourceCursor-rows+1, 0)
	end = min(start+rows, len(h.sources))
	return
}

// renderCompactionHistorySourceRow renders one source row. The selected row
// is the only one that stands out, and a source without a local snapshot says
// so instead of pretending to be openable.
func (m *Model) renderCompactionHistorySourceRow(i int, source compactionHistorySource, width int) string {
	h := &m.compactionHistory
	pointer := "  "
	if i == h.sourceCursor {
		pointer = "> "
	}
	status := compactionHistorySourceMissing
	if source.available {
		status = compactionHistorySourceAvailable
	}
	text := fmt.Sprintf("%s%s · %s", pointer, compactionHistorySourceLabel(source), status)
	text = terminaltext.Truncate(text, width)
	switch {
	case i == h.sourceCursor:
		return currentStyles.StatusHintStyle.Render(text)
	case source.available:
		return currentStyles.DimStyle.Render(text)
	default:
		return currentStyles.DimStyle.Render(text)
	}
}

// handleCompactionHistorySourceKey routes a key while the source list owns the
// screen. Every key is consumed here: the source list is a read-only browser,
// so nothing may reach the live session.
func (m *Model) handleCompactionHistorySourceKey(msg tea.KeyMsg, key string) tea.Cmd {
	h := &m.compactionHistory
	switch key {
	case "esc", "q":
		return m.closeCompactionHistory()
	case "up", "k":
		if h.sourceCursor > 0 {
			h.sourceCursor--
		}
	case "down", "j":
		if h.sourceCursor < len(h.sources)-1 {
			h.sourceCursor++
		}
	case "home":
		h.sourceCursor = 0
	case "end":
		h.sourceCursor = max(len(h.sources)-1, 0)
	case "enter":
		return m.openCompactionHistorySource(h.sourceCursor)
	}
	m.invalidateDrawCaches()
	return nil
}

// compactionHistorySourceRowAt maps a screen row inside the source list onto
// the source it shows. The list starts after its header and the blank line
// below it.
func (m *Model) compactionHistorySourceRowAt(row int) (int, bool) {
	start, end, header := m.compactionHistorySourceWindow()
	index := row - header + start
	if row < header || index < start || index >= end {
		return 0, false
	}
	return index, true
}

// clickCompactionHistorySource handles a click inside the source list: it
// selects the row, and it opens the source when the row was already selected.
// A source without a local snapshot only reports that it cannot be opened.
func (m *Model) clickCompactionHistorySource(row int) tea.Cmd {
	h := &m.compactionHistory
	index, ok := m.compactionHistorySourceRowAt(row)
	if !ok {
		return nil
	}
	if index == h.sourceCursor {
		return m.openCompactionHistorySource(index)
	}
	h.sourceCursor = index
	m.invalidateDrawCaches()
	return nil
}
