package tui

func (v *Viewport) enforceHotBudget() {
	if v.spill == nil || v.maxHotBytes <= 0 {
		v.hotBudgetDirty = false
		v.hotBytesDirty = false
		return
	}
	if !v.hotBudgetDirty {
		return
	}
	defer func() {
		v.hotBudgetDirty = false
		v.hotBytesDirty = false
	}()
	if v.hotBytesDirty {
		v.recomputeHotBytes()
	}
	visible, ok := v.visibleWindowBlockIDsCachedOnly()
	if !ok {
		visible = v.visibleWindowBlockIDs()
	}
	for v.hotBytes > v.maxHotBytes {
		var candidate *Block
		for _, block := range v.blocks {
			if block == nil || block.spillCold {
				continue
			}
			if _, ok := visible[block.ID]; ok {
				continue
			}
			if block.Streaming || block.Focused || block.UserLocalShellPending {
				continue
			}
			if block.Type == BlockToolCall && !block.ResultDone {
				continue
			}
			if v.blockTooLargeToSpill(block) {
				continue
			}
			if candidate == nil || block.lastAccess < candidate.lastAccess {
				candidate = block
			}
		}
		if candidate == nil {
			return
		}
		if !v.spillBlock(candidate) {
			return
		}
	}
}

// blockTooLargeToSpill reports whether a block is too large to spill to disk.
// Materializing such a block from the spill store on scroll-back costs a full
// disk read, JSON decode, and whole-block re-render (hundreds of ms for a
// multi-hundred-KB card), so keeping it hot avoids a visible scroll hitch even
// though it consumes more hot-budget memory.
//
// The exemption is unconditional, so hot bytes can settle above maxHotBytes:
// enforceHotBudget stops when every remaining candidate is exempt. The excess
// is bounded by how many such blocks a session actually produces, not by the
// budget, and the gap is widest under the idle budget (a quarter of the
// baseline, floor 1 MiB), where one exempt block can exceed the whole budget.
func (v *Viewport) blockTooLargeToSpill(block *Block) bool {
	if block == nil {
		return false
	}
	const spillSkipBytes = 4 << 20 // 4 MiB
	return block.estimatedHotBytes() > spillSkipBytes
}

func (v *Viewport) recomputeHotBytes() {
	var total int64
	for _, block := range v.blocks {
		total += block.estimatedHotBytes()
	}
	v.hotBytes = total
}

func (v *Viewport) visibleWindowBlockIDsCachedOnly() (map[int]struct{}, bool) {
	ids := make(map[int]struct{})
	blocks := v.visibleBlocks()
	if len(blocks) == 0 {
		return ids, true
	}
	if !v.blockPositionCacheValid(blocks) {
		return nil, false
	}
	windowStart := v.offset
	windowEnd := v.offset + v.height
	currentLine := 0
	for i, block := range blocks {
		span := v.blockSpansCache[i]
		if span <= 0 {
			return nil, false
		}
		blockStart := currentLine
		blockEnd := currentLine + span
		if blockEnd > windowStart && blockStart < windowEnd {
			ids[block.ID] = struct{}{}
		}
		currentLine = blockEnd
		if currentLine >= windowEnd {
			break
		}
	}
	return ids, true
}

func (v *Viewport) enforceHotBudgetCachedOnly() {
	if v.spill == nil || v.maxHotBytes <= 0 {
		v.hotBudgetDirty = false
		v.hotBytesDirty = false
		return
	}
	if !v.hotBudgetDirty {
		return
	}
	if v.hotBytesDirty {
		v.recomputeHotBytes()
		v.hotBytesDirty = false
	}
	visible, ok := v.visibleWindowBlockIDsCachedOnly()
	if !ok {
		return
	}
	for v.hotBytes > v.maxHotBytes {
		var candidate *Block
		for _, block := range v.blocks {
			if block == nil || block.spillCold {
				continue
			}
			if _, keepVisible := visible[block.ID]; keepVisible {
				continue
			}
			if v.blockTooLargeToSpill(block) {
				continue
			}
			if candidate == nil || block.lastAccess < candidate.lastAccess {
				candidate = block
			}
		}
		if candidate == nil {
			break
		}
		if !v.spillBlock(candidate) {
			break
		}
	}
	v.hotBudgetDirty = false
}

func (v *Viewport) visibleWindowBlockIDs() map[int]struct{} {
	ids := make(map[int]struct{})
	blocks := v.visibleBlocks()
	windowStart := v.offset
	windowEnd := v.offset + v.height
	currentLine := 0
	for i, block := range blocks {
		span := 0
		if v.blockSpansCache != nil && i < len(v.blockSpansCache) {
			span = v.blockSpansCache[i]
		}
		if span <= 0 {
			span = v.blockSpanLines(block)
		}
		blockStart := currentLine
		blockEnd := currentLine + span
		if blockEnd > windowStart && blockStart < windowEnd {
			ids[block.ID] = struct{}{}
		}
		currentLine = blockEnd
		if currentLine >= windowEnd {
			break
		}
	}
	return ids
}
