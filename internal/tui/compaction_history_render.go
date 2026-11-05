package tui

import (
	"fmt"
	"image"
	"strings"

	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

// contentTop returns the screen row where the cards start.
func (h *compactionHistoryState) contentTop() int {
	return h.area.Min.Y
}

func (m *Model) compactionHistoryContentHeight() int {
	return max(m.compactionHistoryArea().Dy(), 1)
}

// Derive from current dimensions: a resize may arrive before the next Draw.
func (m *Model) compactionHistoryArea() image.Rectangle {
	return m.generateLayout(m.width, m.height).main
}

// History owns the message area; the sidebar and status retain their renderers.
func (m *Model) renderCompactionHistory(area image.Rectangle) string {
	h := &m.compactionHistory
	if area.Dx() <= 0 || area.Dy() <= 0 {
		return ""
	}
	h.area = area
	// Width changes belong to the bounded reflow in Update. Search and toast
	// rows only change the available height here.
	h.viewport.SetSize(h.viewport.width, area.Dy())
	content := strings.Split(m.renderCompactionHistoryContent(), "\n")
	return strings.Join(content[:min(len(content), area.Dy())], "\n")
}

func (m *Model) compactionHistoryModeLabel() string {
	h := &m.compactionHistory
	if m.width < 40 {
		return "HISTORY"
	}
	if h.sourceListActive {
		return "HISTORY sources"
	}
	if h.current >= 0 {
		return fmt.Sprintf("HISTORY %d/%d", h.current+1, len(h.snapshots))
	}
	return "HISTORY"
}

// File numbers identify compaction attempts, independently of list positions.
func (m *Model) compactionHistoryStatusText() string {
	h := &m.compactionHistory
	if h.notice != "" {
		return h.notice
	}
	if len(h.issues) > 0 {
		return fmt.Sprintf("%d unavailable snapshots; #%d: %v", len(h.issues), h.issues[0].Index, h.issues[0].Err)
	}
	if h.progress != "" {
		return h.progress
	}
	if state := m.compactionHistorySessionState(); strings.HasPrefix(state, "waiting") {
		return "Session " + state
	}
	if h.loading {
		return "Reading the compaction history…"
	}
	var parts []string
	if !h.sourceListActive && h.current >= 0 && h.current < len(h.snapshots) {
		parts = append(parts, fmt.Sprintf("snapshot #%d", h.snapshots[h.current].Index))
	}
	if !h.sourceListActive && h.referencedPosition >= 0 && h.current == h.referencedPosition {
		parts = append(parts, "referenced")
	}
	parts = append(parts, "Session "+m.compactionHistorySessionState())
	return strings.Join(parts, " · ")
}

// renderCompactionHistoryContent renders the cards of the displayed snapshot.
func (m *Model) renderCompactionHistoryContent() string {
	h := &m.compactionHistory
	if h.viewport == nil {
		return ""
	}
	if h.loading {
		return currentStyles.DimStyle.Render("Reading the compaction history…")
	}
	if h.sourceListActive {
		return m.renderCompactionHistorySources()
	}
	// During a reflow the already-installed cards are real, readable content:
	// show them instead of a progress placeholder. The rest arrive in bounded
	// batches behind this frame.
	if h.progress != "" && h.installedCount == 0 {
		return currentStyles.DimStyle.Render(h.progress)
	}
	if len(h.snapshots) == 0 {
		return currentStyles.DimStyle.Render("No compaction history snapshot is available")
	}
	searchBlockIndex, searchInnerOffset, searchQuery := m.compactionHistorySearchArgs()
	return h.viewport.Render("", h.selection.rangePtr(), searchBlockIndex, searchInnerOffset, searchQuery)
}

// compactionHistorySearchArgs returns the highlight arguments of the current
// match.
func (m *Model) compactionHistorySearchArgs() (int, int, string) {
	h := &m.compactionHistory
	match, ok := h.search.CurrentMatch()
	if !ok {
		return -1, 0, h.searchQuery
	}
	blocks := m.compactionHistoryBlocks()
	for i, b := range blocks {
		if b != nil && b.ID == match.BlockID {
			return i, match.InnerOffset, h.searchQuery
		}
	}
	return -1, 0, h.searchQuery
}

func (m *Model) renderCompactionHistorySearch(width int) string {
	h := &m.compactionHistory
	counter := ""
	if h.search.Active {
		total := len(h.search.Matches)
		current := h.search.Current + 1
		if total == 0 {
			current = 0
		}
		counter = fmt.Sprintf(" [%d/%d]", current, total)
	}
	h.searchInput.SetWidth(max(width-3-ansi.StringWidth(counter), 1))
	return " " + terminaltext.Truncate(h.searchInput.View()+counter, max(width-2, 1))
}

// compactionHistorySessionState describes the live session in one phrase, so
// the user can tell that the session kept running while the history was open.
func (m *Model) compactionHistorySessionState() string {
	switch {
	case m.confirm.request != nil:
		return "waiting for confirmation"
	case m.question.request != nil:
		return "waiting for an answer"
	case len(m.pendingDialogs) > 0:
		return "waiting for a decision"
	case m.isFocusedAgentBusy():
		return "running"
	default:
		return "idle"
	}
}
