package tui

func (m *Model) appendViewportBlock(block *Block) {
	if m == nil || m.viewport == nil || block == nil {
		return
	}
	m.viewport.AppendBlock(block)
	m.syncStartupDeferredTranscriptAfterViewportAppend()
}

func (m *Model) updateViewportBlock(block *Block) {
	if m == nil || m.viewport == nil || block == nil {
		return
	}
	m.viewport.UpdateBlock(block.ID)
	m.syncStartupDeferredTranscriptBlock(block)
}

func (m *Model) removeViewportBlockByID(blockID int) {
	if m == nil || m.viewport == nil || blockID < 0 {
		return
	}
	m.viewport.RemoveBlockByID(blockID)
	m.syncStartupDeferredTranscriptAfterViewportRemove(blockID)
}

func (m *Model) startupDeferredTranscriptBlockIndex(blockID int) int {
	state := m.startupDeferredTranscript
	if state == nil || blockID < 0 {
		return -1
	}
	for i, block := range state.allBlocks {
		if block != nil && block.ID == blockID {
			return i
		}
	}
	return -1
}

func (m *Model) startupDeferredTranscriptMetaIndex(blockID int) int {
	state := m.startupDeferredTranscript
	if state == nil || blockID < 0 {
		return -1
	}
	for i, meta := range state.blockMeta {
		if meta.BlockID == blockID {
			return i
		}
	}
	return -1
}

func (m *Model) startupDeferredMetaWidth() int {
	if m != nil && m.viewport != nil && m.viewport.width > 0 {
		return m.viewport.width
	}
	return 80
}

func startupDeferredMetaForBlock(block *Block, width int) startupDeferredBlockMeta {
	meta := buildStartupDeferredBlockMeta([]*Block{block}, width)
	if len(meta) > 0 {
		return meta[0]
	}
	return startupDeferredBlockMeta{
		BlockID: block.ID,
		Type:    block.Type,
		Summary: block.Summary(),
	}
}

func (m *Model) syncStartupDeferredTranscriptWindowToViewport() {
	state := m.startupDeferredTranscript
	if state == nil {
		return
	}
	total := len(state.allBlocks)
	if total == 0 {
		state.windowStart = 0
		state.windowEnd = 0
		state.hiddenBlocks = 0
		state.anchorBlockID = -1
		return
	}
	state.hiddenBlocks = max(0, total-startupTranscriptTailBlocks)
	if m.viewport == nil || len(m.viewport.blocks) == 0 {
		state.windowEnd = min(state.windowEnd, total)
		state.windowStart = min(state.windowStart, state.windowEnd)
		return
	}
	firstID := m.viewport.blocks[0].ID
	lastID := m.viewport.blocks[len(m.viewport.blocks)-1].ID
	start, end := -1, -1
	for i, block := range state.allBlocks {
		if block == nil {
			continue
		}
		if start < 0 && block.ID == firstID {
			start = i
		}
		if block.ID == lastID {
			end = i + 1
		}
	}
	if start >= 0 && end >= start {
		state.windowStart = start
		state.windowEnd = end
	}
	if m.startupDeferredTranscriptBlockIndex(state.anchorBlockID) < 0 {
		state.anchorBlockID = firstID
	}
}

func (m *Model) syncStartupDeferredTranscriptBlock(block *Block) {
	state := m.startupDeferredTranscript
	if state == nil || block == nil {
		return
	}
	idx := m.startupDeferredTranscriptBlockIndex(block.ID)
	if idx < 0 {
		return
	}
	clone := cloneBlockForDeferredSource(block)
	state.allBlocks[idx] = clone
	metaIdx := m.startupDeferredTranscriptMetaIndex(block.ID)
	if metaIdx >= 0 {
		state.blockMeta[metaIdx] = startupDeferredMetaForBlock(clone, m.startupDeferredMetaWidth())
	}
}

func (m *Model) syncStartupDeferredTranscriptAfterViewportRemove(blockID int) {
	state := m.startupDeferredTranscript
	if state == nil || blockID < 0 {
		return
	}
	if idx := m.startupDeferredTranscriptBlockIndex(blockID); idx >= 0 {
		state.allBlocks = append(state.allBlocks[:idx], state.allBlocks[idx+1:]...)
	}
	if metaIdx := m.startupDeferredTranscriptMetaIndex(blockID); metaIdx >= 0 {
		state.blockMeta = append(state.blockMeta[:metaIdx], state.blockMeta[metaIdx+1:]...)
	}
	m.syncStartupDeferredTranscriptWindowToViewport()
}

func (m *Model) syncStartupDeferredTranscriptAfterViewportAppend() {
	state := m.startupDeferredTranscript
	if state == nil || m.viewport == nil || len(m.viewport.blocks) == 0 {
		return
	}
	block := m.viewport.blocks[len(m.viewport.blocks)-1]
	if block == nil {
		return
	}
	if idx := m.startupDeferredTranscriptBlockIndex(block.ID); idx >= 0 {
		m.syncStartupDeferredTranscriptBlock(block)
		m.syncStartupDeferredTranscriptWindowToViewport()
		return
	}
	clone := cloneBlockForDeferredSource(block)
	state.allBlocks = append(state.allBlocks, clone)
	state.blockMeta = append(state.blockMeta, startupDeferredMetaForBlock(clone, m.startupDeferredMetaWidth()))
	state.anchorBlockID = block.ID
	m.syncStartupDeferredTranscriptWindowToViewport()
	m.recordTUIDiagnostic("startup-deferred-live-append", "block=%d type=%s total=%d visible=%d window=[%d,%d)", block.ID, debugBlockTypeString(block.Type), len(state.allBlocks), len(m.viewport.blocks), state.windowStart, state.windowEnd)
}
