package tui

func (v *Viewport) prepareBlock(block *Block) {
	if block == nil {
		return
	}
	block.spillStore = v.spill
	block.spillRecover = v.recoverSpilledBlock
	if block.spillLineCounts == nil {
		block.spillLineCounts = make(map[int]int)
	}
	v.accessSeq++
	block.lastAccess = v.accessSeq
	if !block.spillCold {
		v.hotBytes += block.estimatedHotBytes()
	}
}

func (v *Viewport) materialize(block *Block) *Block {
	if block == nil {
		return nil
	}
	wasCold := block.spillCold
	_ = block.ensureMaterialized()
	if wasCold && !block.spillCold {
		v.hotBytes += block.estimatedHotBytes()
		v.markHotBudgetDirty()
	}
	v.accessSeq++
	block.lastAccess = v.accessSeq
	return block
}

func (v *Viewport) recoverSpilledBlock(blockID int) *Block {
	if v == nil || v.spillRecovery == nil {
		return nil
	}
	prevOffset := v.offset
	prevSticky := v.sticky
	blocks := v.spillRecovery()
	if len(blocks) == 0 {
		return nil
	}
	v.ReplaceBlocks(blocks)
	v.sticky = prevSticky
	if !prevSticky {
		v.offset = prevOffset
		v.clampOffset()
	}
	for _, block := range v.blocks {
		if block != nil && block.ID == blockID {
			return block
		}
	}
	return nil
}

func (v *Viewport) lineCount(block *Block, width int) int {
	if block == nil {
		return 0
	}
	if block.spillCold {
		if lc, ok := block.spillLineCounts[width]; ok {
			return lc
		}
		inspect, temporary := block.inspectionBlock()
		if inspect == nil {
			return 0
		}
		lc := inspect.LineCount(width)
		if block.spillLineCounts == nil {
			block.spillLineCounts = make(map[int]int)
		}
		block.spillLineCounts[width] = lc
		if temporary {
			inspect.InvalidateCache()
		}
		return lc
	}
	return block.LineCount(width)
}
