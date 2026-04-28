package tui

func (m *Model) clearActiveSearch() {
	if m == nil || !m.search.State.Active {
		return
	}
	ClearSearch(&m.search.State)
	m.search.Input.Blur()
	m.recalcViewportSize()
}

func (m *Model) searchSessionPreservedByKey(key string) bool {
	if m == nil {
		return false
	}
	return keyMatches(key, m.keyMap.SearchNext) || keyMatches(key, m.keyMap.SearchPrev)
}

func (m *Model) maybeClearSearchSessionForNormalKey(key string) {
	if m == nil || m.mode != ModeNormal || !m.search.State.Active {
		return
	}
	if key == "esc" || m.searchSessionPreservedByKey(key) {
		return
	}
	m.clearActiveSearch()
}

func blockIndexByID(blocks []*Block, id int) int {
	if id < 0 {
		return -1
	}
	for i, block := range blocks {
		if block != nil && block.ID == id {
			return i
		}
	}
	return -1
}

func deferredMetaIndexByID(meta []startupDeferredBlockMeta, id int) int {
	if id < 0 {
		return -1
	}
	for i, blockMeta := range meta {
		if blockMeta.BlockID == id {
			return i
		}
	}
	return -1
}

func initialSearchCurrentIndex(matches []MatchPosition, anchorBlockIndex int) int {
	if len(matches) == 0 {
		return -1
	}
	if anchorBlockIndex < 0 {
		return 0
	}
	for i, match := range matches {
		if match.BlockIndex >= anchorBlockIndex {
			return i
		}
	}
	return 0
}

func (m *Model) searchAnchorIndexInVisibleBlocks(blocks []*Block) int {
	if idx := blockIndexByID(blocks, m.focusedBlockID); idx >= 0 {
		return idx
	}
	if m.viewport != nil {
		if block := m.viewport.GetBlockAtOffset(); block != nil {
			if idx := blockIndexByID(blocks, block.ID); idx >= 0 {
				return idx
			}
		}
	}
	if len(blocks) > 0 {
		return 0
	}
	return -1
}

func (m *Model) searchAnchorIndexInDeferredMeta() int {
	state := m.startupDeferredTranscript
	if state == nil {
		return -1
	}
	if idx := deferredMetaIndexByID(state.blockMeta, m.focusedBlockID); idx >= 0 {
		return idx
	}
	if m.viewport != nil {
		if block := m.viewport.GetBlockAtOffset(); block != nil {
			if idx := deferredMetaIndexByID(state.blockMeta, block.ID); idx >= 0 {
				return idx
			}
		}
	}
	if len(state.blockMeta) > 0 {
		return 0
	}
	return -1
}
