package tui

import "github.com/keakon/chord/internal/tools"

func (m *Model) startupDeferredTranscriptAtTail() bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	return state.windowEnd >= len(state.allBlocks)
}

func (m *Model) startupDeferredTranscriptPinnedToTail() bool {
	if m == nil || m.viewport == nil || !m.hasDeferredStartupTranscript() {
		return false
	}
	return m.startupDeferredTranscriptAtTail() && (m.viewport.sticky || m.viewport.atBottom())
}

func (m *Model) appendViewportBlock(block *Block) {
	if m == nil || m.viewport == nil || block == nil {
		return
	}
	m.assignDisplaySequence(block)
	m.viewport.AppendBlock(block)
	m.syncStartupDeferredTranscriptAfterViewportAppend()
}

func displaySequenceAgentKey(agentID string) string {
	if agentID == "main" {
		return ""
	}
	return agentID
}

func (m *Model) assignDisplaySequence(block *Block) {
	if m == nil || block == nil || block.DisplaySequence > 0 {
		return
	}
	if m.lastDisplaySequence == nil {
		m.lastDisplaySequence = make(map[string]int)
	}
	key := displaySequenceAgentKey(block.AgentID)
	m.lastDisplaySequence[key]++
	block.DisplaySequence = m.lastDisplaySequence[key]
}

func (m *Model) setTranscriptDisplaySequences(blocks []*Block, agentID string) {
	if m == nil {
		return
	}
	if m.lastDisplaySequence == nil {
		m.lastDisplaySequence = make(map[string]int)
	}
	key := displaySequenceAgentKey(agentID)
	sequence := 0
	for _, block := range blocks {
		if block == nil {
			continue
		}
		sequence++
		block.DisplaySequence = sequence
	}
	m.lastDisplaySequence[key] = sequence
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

func (m *Model) findDeferredBlock(match func(*Block) bool) (*Block, bool) {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || match == nil {
		return nil, false
	}
	for _, block := range state.allBlocks {
		if block != nil && match(block) {
			return block, true
		}
	}
	return nil, false
}

func (m *Model) findToolBlockByToolID(toolID string) (*Block, bool) {
	if m != nil && m.viewport != nil && toolID != "" {
		if block, ok := m.viewport.FindBlockByToolID(toolID); ok {
			return block, true
		}
	}
	return m.findDeferredBlock(func(block *Block) bool {
		return block.Type == BlockToolCall && block.ToolID == toolID
	})
}

func (m *Model) findLastPendingToolBlockByName(toolName string) (*Block, bool) {
	toolName = tools.NormalizeName(toolName)
	if m != nil && m.viewport != nil && toolName != "" {
		if block, ok := m.viewport.FindLastPendingToolBlockByName(toolName); ok {
			return block, true
		}
	}
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || toolName == "" {
		return nil, false
	}
	for i := len(state.allBlocks) - 1; i >= 0; i-- {
		block := state.allBlocks[i]
		if block != nil && block.Type == BlockToolCall && tools.NormalizeName(block.ToolName) == toolName && !block.ResultDone {
			return block, true
		}
	}
	return nil, false
}

func (m *Model) findStatusBlockByBackgroundObject(backgroundID string) (*Block, bool) {
	if m != nil && m.viewport != nil && backgroundID != "" {
		if block, ok := m.viewport.FindStatusBlockByBackgroundObject(backgroundID); ok {
			return block, true
		}
	}
	return m.findDeferredBlock(func(block *Block) bool {
		return block.Type == BlockStatus && block.BackgroundObjectID == backgroundID
	})
}

func (m *Model) findBlockByLinkedAgent(agentID string) (*Block, bool) {
	if m != nil && m.viewport != nil && agentID != "" {
		if block, ok := m.viewport.FindBlockByLinkedAgent(agentID); ok {
			return block, true
		}
	}
	return m.findDeferredBlock(func(block *Block) bool {
		return block.Type == BlockToolCall && block.LinkedAgentID == agentID
	})
}

func (m *Model) findBlockByLinkedTask(taskID string) (*Block, bool) {
	if m != nil && m.viewport != nil && taskID != "" {
		if block, ok := m.viewport.FindBlockByLinkedTask(taskID); ok {
			return block, true
		}
	}
	return m.findDeferredBlock(func(block *Block) bool {
		return block.Type == BlockToolCall && block.LinkedTaskID == taskID
	})
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
		visibleCount := len(m.viewport.blocks)
		if end-start == visibleCount {
			state.windowStart = start
			state.windowEnd = end
		}
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

func (m *Model) rebindLiveViewportBlocks() {
	if m == nil || m.viewport == nil {
		return
	}
	if m.currentAssistantBlock != nil {
		if rebound := m.viewport.GetFocusedBlock(m.currentAssistantBlock.ID); rebound != nil {
			m.currentAssistantBlock = rebound
		}
	}
	if m.currentThinkingBlock != nil {
		if rebound := m.viewport.GetFocusedBlock(m.currentThinkingBlock.ID); rebound != nil {
			m.currentThinkingBlock = rebound
		}
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
	wasShowingTail := state.windowEnd >= len(state.allBlocks)-1
	forceTailWindow := block.Type == BlockUser
	if wasShowingTail || forceTailWindow {
		state.windowEnd = len(state.allBlocks)
		state.windowStart = max(0, state.windowEnd-startupTranscriptTailBlocks)
		if forceTailWindow && !wasShowingTail {
			windowed := m.startupDeferredWindowBlocks(state, state.windowStart, state.windowEnd)
			if len(windowed) > 0 {
				m.viewport.sticky = false
				m.viewport.ReplaceBlocks(windowed)
				m.rebindLiveViewportBlocks()
				m.recalcViewportSize()
				m.viewport.ScrollToBottom()
				m.viewport.sticky = true
			}
		} else if forceTailWindow && wasShowingTail {
			// User was already at tail and sent a new message. The viewport already
			// has the new block appended, but we need to ensure it's scrolled to bottom
			// in case sticky was false (user scrolled up slightly within the tail window).
			if !m.viewport.atBottom() {
				m.viewport.ScrollToBottom()
			}
			m.viewport.sticky = true
		}
		m.syncStartupDeferredTranscriptWindowToViewport()
	} else {
		state.hiddenBlocks = max(0, len(state.allBlocks)-startupTranscriptTailBlocks)
	}
	m.recordTUIDiagnostic("startup-deferred-live-append", "block=%d type=%s total=%d visible=%d window=[%d,%d) tail=%t force_tail=%t", block.ID, debugBlockTypeString(block.Type), len(state.allBlocks), len(m.viewport.blocks), state.windowStart, state.windowEnd, wasShowingTail, forceTailWindow)
}
