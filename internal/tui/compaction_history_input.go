package tui

import (
	"image"
	"strings"

	"github.com/keakon/bubbles/v2/textinput"
	tea "github.com/keakon/bubbletea/v2"
)

// The history layer owns every key and every mouse event while it is open.
// Nothing it receives reaches the live session: no message is sent, no queue
// entry is changed, no request is answered, and Ctrl+C leaves the layer
// instead of quitting the process.

// handleCompactionHistoryKey routes a key to the history layer.
func (m *Model) handleCompactionHistoryKey(msg tea.KeyMsg) tea.Cmd {
	h := &m.compactionHistory
	if !h.active {
		return nil
	}
	key := msg.String()
	// Ctrl+C leaves the history from every sub-state, including the search
	// field. It never reaches the quit handling of the live session.
	if isCtrlC(msg) {
		return m.closeCompactionHistory()
	}
	if isSuperC(msg) {
		return m.copyCompactionHistoryBlock()
	}
	if h.searching {
		return m.handleCompactionHistorySearchKey(msg, key)
	}
	if h.sourceListActive {
		// The source list owns every key while it is on screen: it is a
		// read-only browser and must not reach the live session.
		return m.handleCompactionHistorySourceKey(msg, key)
	}
	if keyMatches(key, m.keyMap.CompactionHistory) {
		return m.stepCompactionHistorySnapshot(-1)
	}
	if keyMatches(key, m.keyMap.CompactionHistoryNext) {
		return m.stepCompactionHistorySnapshot(1)
	}
	switch key {
	case "esc", "q":
		return m.closeCompactionHistory()
	case "left", "h":
		return m.stepCompactionHistorySnapshot(-1)
	case "right", "l":
		return m.stepCompactionHistorySnapshot(1)
	case "up", "k":
		m.focusCompactionHistoryBlock(-1, focusCompactionHistorySelectable)
	case "down", "j":
		m.focusCompactionHistoryBlock(1, focusCompactionHistorySelectable)
	case "ctrl+f", "pgdown":
		m.scrollCompactionHistory(max(1, m.compactionHistory.viewport.height-3))
	case "ctrl+b", "pgup":
		m.scrollCompactionHistory(-max(1, m.compactionHistory.viewport.height-3))
	case "home":
		m.compactionHistory.viewport.ScrollToTop()
		m.setCompactionHistoryFocus(-1)
	case "end", "G":
		m.compactionHistory.viewport.ScrollToBottom()
	case "g":
		m.compactionHistory.viewport.ScrollToTop()
		m.setCompactionHistoryFocus(-1)
	case "[":
		m.focusCompactionHistorySameType(-1)
	case "]":
		m.focusCompactionHistorySameType(1)
	case "o", "enter", "space":
		cmd := m.toggleCompactionHistoryBlock()
		m.invalidateDrawCaches()
		return cmd
	case "y":
		return m.copyCompactionHistoryBlock()
	case "/":
		return m.beginCompactionHistorySearch()
	case "n":
		m.stepCompactionHistoryMatch(true)
	case "N":
		m.stepCompactionHistoryMatch(false)
	}
	m.invalidateDrawCaches()
	return nil
}

// handleCompactionHistorySearchKey routes a key while the query field has
// focus. In this state q and the arrow keys belong to the query.
func (m *Model) handleCompactionHistorySearchKey(msg tea.KeyMsg, key string) tea.Cmd {
	h := &m.compactionHistory
	switch key {
	case "esc":
		// The first Esc leaves the search, not the history.
		m.cancelCompactionHistorySearch()
		h.searching = false
		h.searchInput.Blur()
		m.invalidateDrawCaches()
		return nil
	case "enter":
		query := strings.TrimSpace(h.searchInput.Value())
		h.searching = false
		h.searchInput.Blur()
		cmd := m.startCompactionHistorySearch(query)
		m.invalidateDrawCaches()
		return cmd
	}
	var cmd tea.Cmd
	h.searchInput, cmd = h.searchInput.Update(msg)
	return cmd
}

// beginCompactionHistorySearch opens the query field of the history layer.
func (m *Model) beginCompactionHistorySearch() tea.Cmd {
	h := &m.compactionHistory
	h.searching = true
	h.searchInput = newCompactionHistorySearchInput()
	h.searchInput.SetWidth(max(m.width-40, 10))
	m.invalidateDrawCaches()
	return textinput.Blink
}

// scrollCompactionHistory scrolls the history viewport by a number of lines.
func (m *Model) scrollCompactionHistory(delta int) {
	h := &m.compactionHistory
	if h.viewport == nil || delta == 0 {
		return
	}
	if delta > 0 {
		h.viewport.ScrollDown(delta)
		return
	}
	h.viewport.ScrollUp(-delta)
}

// stepCompactionHistorySnapshot moves along the snapshots and back to the live
// session after the newest one, including while a read is still pending.
func (m *Model) stepCompactionHistorySnapshot(delta int) tea.Cmd {
	h := &m.compactionHistory
	if h.sourceListActive {
		// The source list is not a snapshot yet: stepping through the
		// session's snapshots here would silently open a source the
		// checkpoint never named.
		return nil
	}
	if len(h.snapshots) == 0 {
		if delta > 0 {
			return m.closeCompactionHistory()
		}
		return nil
	}
	position := h.current
	if h.loading && h.pendingIndex >= 0 {
		position = h.pendingIndex
	}
	next := position + delta
	if next < 0 {
		h.notice = "Already at the earliest snapshot"
		m.invalidateDrawCaches()
		return nil
	}
	if next >= len(h.snapshots) {
		return m.closeCompactionHistory()
	}
	h.notice = ""
	return m.requestCompactionHistorySnapshot(next)
}

// focusCompactionHistorySelectable is the focus predicate of the card-by-card
// navigation: every card the live view can select.
func focusCompactionHistorySelectable(b *Block) bool {
	return b != nil && isSelectableBlockType(b.Type)
}

func (m *Model) focusCompactionHistorySameType(dir int) {
	h := &m.compactionHistory
	block := h.viewport.GetFocusedBlock(h.focusedBlockID)
	if block == nil {
		block = h.viewport.GetBlockAtOffset()
	}
	if block == nil {
		return
	}
	blockType := block.Type
	m.focusCompactionHistoryBlock(dir, func(candidate *Block) bool {
		return candidate != nil && candidate.Type == blockType
	})
}

// focusCompactionHistoryBlock moves the focus to the next or previous card
// that satisfies the predicate, and brings it into view.
func (m *Model) focusCompactionHistoryBlock(dir int, match func(*Block) bool) {
	h := &m.compactionHistory
	blocks := m.compactionHistoryBlocks()
	if len(blocks) == 0 {
		return
	}
	nextID := focusNextMatchingBlockID(blocks, h.focusedBlockID, dir, match)
	if block := h.viewport.GetFocusedBlock(nextID); block != nil {
		m.setCompactionHistoryFocus(block.ID)
		h.viewport.scrollToBlock(block.ID)
	}
}

// toggleCompactionHistoryBlock folds or unfolds the focused card.
func (m *Model) toggleCompactionHistoryBlock() tea.Cmd {
	h := &m.compactionHistory
	if h.viewport == nil || h.focusedBlockID < 0 {
		return nil
	}
	block := h.viewport.GetFocusedBlock(h.focusedBlockID)
	if block == nil {
		return nil
	}
	if !block.ToggleAtWidth(h.viewport.width) {
		return nil
	}
	h.selection = viewportSelection{}
	h.viewport.noteBlockLineCountMayChange()
	h.viewport.UpdateBlock(block.ID)
	h.viewport.clampOffset()
	if h.searchQuery != "" {
		return m.startCompactionHistorySearch(h.searchQuery)
	}
	return nil
}

// copyCompactionHistoryBlock copies the focused card to the clipboard.
func (m *Model) copyCompactionHistoryBlock() tea.Cmd {
	h := &m.compactionHistory
	if sel := h.selection.rangePtr(); sel != nil {
		if text := h.viewport.ExtractSelectionText(*sel); text != "" {
			return writeClipboardCmd(text, "Selection copied to clipboard")
		}
	}
	return m.copyViewportBlock(h.viewport, h.focusedBlockID, "History card copied to clipboard")
}

// handleCompactionHistoryMouse routes a mouse event to the history layer.
func (m *Model) handleCompactionHistoryMouse(msg tea.MouseMsg) tea.Cmd {
	h := &m.compactionHistory
	if !h.active {
		return nil
	}
	switch msg := msg.(type) {
	case tea.MouseWheelMsg:
		delta := 0
		switch msg.Button {
		case tea.MouseWheelUp:
			delta = -3
		case tea.MouseWheelDown:
			delta = 3
		}
		point := image.Pt(msg.X, msg.Y)
		if point.In(m.layout.infoPanel) {
			m.scrollInfoPanel(delta)
		} else if point.In(h.area) {
			m.scrollCompactionHistory(delta)
		}
		m.invalidateDrawCaches()
		return nil
	case tea.MouseClickMsg:
		return m.handleCompactionHistoryClick(msg)
	case tea.MouseMotionMsg:
		m.updateCompactionHistorySelection(msg.Mouse())
	case tea.MouseReleaseMsg:
		if msg.Button == tea.MouseLeft {
			m.updateCompactionHistorySelection(msg.Mouse())
			h.selection.dragging = false
		}
	}
	m.invalidateDrawCaches()
	return nil
}

// Sidebar clicks may fold sections, but never switch agents or stop jobs.
func (m *Model) handleCompactionHistoryClick(msg tea.MouseClickMsg) tea.Cmd {
	h := &m.compactionHistory
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	if section, ok := m.infoPanelSectionAtPoint(mouse.X, mouse.Y); ok {
		m.toggleInfoPanelSection(section)
		m.invalidateDrawCaches()
		return nil
	}
	if !image.Pt(mouse.X, mouse.Y).In(h.area) {
		return nil
	}
	if h.sourceListActive {
		return m.clickCompactionHistorySource(mouse.Y - h.contentTop())
	}
	m.beginCompactionHistorySelection(mouse)
	m.invalidateDrawCaches()
	return nil
}
