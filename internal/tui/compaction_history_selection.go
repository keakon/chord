package tui

import tea "github.com/keakon/bubbletea/v2"

func (m *Model) compactionHistorySelectionPoint(mouse tea.Mouse) (*Block, int, int) {
	h := &m.compactionHistory
	if h.viewport == nil || h.loading || h.sourceListActive {
		return nil, 0, 0
	}
	row, col := mouse.Y-h.contentTop(), mouse.X-h.area.Min.X
	if row < 0 || row >= h.viewport.height || col < 0 || col >= h.viewport.width {
		return nil, 0, 0
	}
	block, line := h.viewport.GetBlockAndLineAt(h.viewport.offset + row)
	return block, line, col
}

func (m *Model) beginCompactionHistorySelection(mouse tea.Mouse) {
	h := &m.compactionHistory
	block, line, col := m.compactionHistorySelectionPoint(mouse)
	if block == nil {
		h.selection = viewportSelection{}
		return
	}
	m.setCompactionHistoryFocus(block.ID)
	h.selection.begin(h.viewport, block, line, col, mouse)
}

func (m *Model) updateCompactionHistorySelection(mouse tea.Mouse) {
	s := &m.compactionHistory.selection
	if !s.dragging {
		return
	}
	block, line, col := m.compactionHistorySelectionPoint(mouse)
	if block != nil {
		s.extend(block.ID, line, col)
	}
}

func (m *Model) setCompactionHistoryFocus(id int) {
	h := &m.compactionHistory
	if h.focusedBlockID == id {
		return
	}
	for _, blockID := range []int{h.focusedBlockID, id} {
		if block := h.viewport.GetFocusedBlock(blockID); block != nil {
			block.Focused = blockID == id
			block.InvalidateCache()
			h.viewport.UpdateBlock(block.ID)
		}
	}
	h.focusedBlockID = id
}
