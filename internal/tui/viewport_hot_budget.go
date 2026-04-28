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
	visible := v.visibleWindowBlockIDs()
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
	if len(v.blockSpansCache) != len(blocks) {
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
