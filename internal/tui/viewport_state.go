package tui

const viewportTurnSpacingLines = 1

func (v *Viewport) SwapSpillStore(spill *ViewportSpillStore) *ViewportSpillStore {
	if v == nil || spill == nil || v.spill == spill {
		return nil
	}
	prev := v.spill
	v.spill = spill
	return prev
}

func (v *Viewport) Close() error {
	if v == nil || v.spill == nil {
		return nil
	}
	spill := v.spill
	v.spill = nil
	return spill.Close()
}

func (v *Viewport) SetSpillRecovery(fn func() []*Block) {
	v.spillRecovery = fn
}

func (v *Viewport) SetSize(width, height int) {
	if width == v.width && height == v.height {
		return
	}
	wasSticky := v.sticky
	v.width = width
	v.height = height
	v.markHotBudgetDirty()
	v.recalcTotalLines()
	if wasSticky {
		v.scrollToEnd()
	} else {
		v.clampOffset()
	}
	v.noteBlockLineCountMayChange()
}

func (v *Viewport) SetWidth(width int) {
	if width == v.width {
		return
	}
	wasSticky := v.sticky
	v.width = width
	v.markHotBudgetDirty()
	v.recalcTotalLines()
	if wasSticky {
		v.scrollToEnd()
	} else {
		v.clampOffset()
	}
	v.noteBlockLineCountMayChange()
}

func (v *Viewport) SetFilter(agentID string) {
	if agentID == v.filterAgentID {
		return
	}
	v.filterAgentID = agentID
	v.invalidateVisibleBlocksCache()
	v.markHotBudgetDirty()
	for _, block := range v.blocks {
		block.viewportCache = nil
		block.viewportCacheWidth = 0
	}
	v.recalcTotalLines()
	v.noteBlockLineCountMayChange()
	v.clampOffset()
	if v.sticky {
		v.scrollToEnd()
	}
	v.enforceHotBudget()
}

func (v *Viewport) visibleBlocks() []*Block {
	if v.visibleBlocksDirty || v.visibleBlocksCache == nil {
		v.visibleBlocksCache = filterBlocksByAgent(v.blocks, v.filterAgentID)
		v.visibleBlocksDirty = false
	}
	return v.visibleBlocksCache
}

func (v *Viewport) invalidateVisibleBlocksCache() {
	v.visibleBlocksCache = nil
	v.visibleBlocksDirty = true
	v.blockStartsCache = nil
	v.blockSpansCache = nil
}

func (v *Viewport) bumpRenderVersion() { v.renderVersion++ }

func (v *Viewport) RenderVersion() uint64 {
	if v == nil {
		return 0
	}
	return v.renderVersion
}

func (v *Viewport) markHotBudgetDirty() {
	v.hotBudgetDirty = true
	v.hotBytesDirty = true
}

func (v *Viewport) markHotBudgetNeedsEnforcement() { v.hotBudgetDirty = true }

func (v *Viewport) invalidateBlockPositionCache() {
	v.blockStartsCache = nil
	v.blockSpansCache = nil
}

func (v *Viewport) invalidateCachesForLineCountChange() {
	v.invalidateBlockPositionCache()
	v.hotBudgetDirty = true
}

func (v *Viewport) noteBlockLineCountMayChange() { v.invalidateCachesForLineCountChange() }

func (v *Viewport) blockSpanLines(block *Block) int {
	if block == nil {
		return 0
	}
	return v.lineCount(block, v.width)
}

func (v *Viewport) isTurnBoundary(prev, curr *Block) bool {
	if prev == nil || curr == nil {
		return false
	}
	return false
}

func (v *Viewport) blockLeadingSpacing(blocks []*Block, blockIndex int) int {
	if blockIndex <= 0 || blockIndex >= len(blocks) {
		return 0
	}
	if v.isTurnBoundary(blocks[blockIndex-1], blocks[blockIndex]) {
		return viewportTurnSpacingLines
	}
	return 0
}

func (v *Viewport) blockSpanAt(blocks []*Block, blockIndex int, block *Block) int {
	if block == nil {
		return 0
	}
	return v.blockLeadingSpacing(blocks, blockIndex) + v.blockSpanLines(block)
}

func (v *Viewport) measuredBlockSpanAt(blocks []*Block, blockIndex int, block *Block) int {
	if block == nil {
		return 0
	}
	return v.blockLeadingSpacing(blocks, blockIndex) + v.measureSpanLines(block)
}

func (v *Viewport) measureSpanLines(block *Block) int {
	if block == nil {
		return 0
	}
	if block.spillCold {
		if lc, ok := block.spillLineCounts[v.width]; ok {
			return lc
		}
		inspect, temporary := block.inspectionBlock()
		if inspect == nil {
			return 0
		}
		lc := inspect.MeasureLineCount(v.width)
		if block.spillLineCounts == nil {
			block.spillLineCounts = make(map[int]int)
		}
		block.spillLineCounts[v.width] = lc
		if temporary {
			inspect.InvalidateCache()
		}
		return lc
	}
	return block.MeasureLineCount(v.width)
}

func (v *Viewport) recalcTotalLines() {
	total := 0
	blocks := v.visibleBlocks()
	starts := make([]int, len(blocks))
	spans := make([]int, len(blocks))
	for i, block := range blocks {
		starts[i] = total
		span := v.blockSpanAt(blocks, i, block)
		spans[i] = span
		total += span
	}
	v.totalLines = total
	v.blockStartsCache = starts
	v.blockSpansCache = spans
	if len(spans) > 0 {
		v.lastBlockSpan = spans[len(spans)-1]
	} else {
		v.lastBlockSpan = 0
	}
}

func (v *Viewport) blockStarts() []int {
	blocks := v.visibleBlocks()
	if len(blocks) == 0 {
		return nil
	}
	if len(v.blockStartsCache) != len(blocks) || len(v.blockSpansCache) != len(blocks) {
		v.recalcTotalLines()
	}
	return v.blockStartsCache
}
