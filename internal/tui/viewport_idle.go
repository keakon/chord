package tui

// viewport_idle.go provides idle-sweep helpers for the viewport.
// These methods are called from app_idle.go during background idle periods.

func (v *Viewport) cachedLineCount(block *Block, width int) (int, bool) {
	if block == nil {
		return 0, false
	}
	if width <= 0 {
		width = v.width
	}
	if width <= 0 {
		return 0, false
	}
	if block.spillCold {
		if block.spillLineCounts == nil {
			return 0, false
		}
		lc, ok := block.spillLineCounts[width]
		return lc, ok
	}
	if block.lineCountCache > 0 && block.lineCacheWidth == width {
		return block.lineCountCache, true
	}
	return 0, false
}

// DropOffScreenCaches clears render caches for blocks that are not currently
// visible in the viewport window. It preserves:
//   - The currently visible window and its immediate neighbors
//   - Blocks that are still streaming or pending
//   - The focused block
//
// This is a lightweight operation: it only clears cache references, it does
// not re-render anything or perform spill-store I/O. Blocks without cached line
// counts stop the sweep conservatively so later block positions are not computed
// from stale line offsets.
func (v *Viewport) DropOffScreenCaches() {
	if v == nil {
		return
	}

	margin := 5 // rows above/below visible window to preserve
	visibleStart := v.offset - margin
	if visibleStart < 0 {
		visibleStart = 0
	}
	visibleEnd := v.offset + v.height + margin

	currentLine := 0
	for _, block := range v.visibleBlocks() {
		blockLines, ok := v.cachedLineCount(block, v.width)
		if !ok {
			return
		}
		blockStart := currentLine
		blockEnd := currentLine + blockLines

		if blockEnd < visibleStart || blockStart > visibleEnd {
			if v.canDropBlockCache(block) {
				block.InvalidateCache()
			}
		}

		currentLine = blockEnd
	}
}

// canDropBlockCache returns true if it's safe to drop a block's render cache.
// Blocks that are still in use (streaming, focused, pending) must not be cleared.
func (v *Viewport) canDropBlockCache(block *Block) bool {
	if block == nil {
		return false
	}
	// Don't clear streaming blocks - they're actively updating.
	if block.Streaming {
		return false
	}
	// Don't clear focused blocks - user might interact with them.
	if block.Focused {
		return false
	}
	// Don't clear blocks with pending local shell - they need to update.
	if block.UserLocalShellPending {
		return false
	}
	// Don't clear unresolved tool cards; idle sweep should preserve pending work
	// even if a future caller bypasses the outer busy-state gate.
	if block.Type == BlockToolCall && !block.ResultDone {
		return false
	}
	return true
}

// ShrinkHotBudget reduces the hot budget during background idle periods,
// causing more blocks to be spilled to disk. The reduced budget stays active
// until RestoreHotBudget is called on focus restore.
func (v *Viewport) ShrinkHotBudget() {
	if v == nil {
		return
	}
	baseBudget := v.baseHotBytes
	if baseBudget <= 0 {
		baseBudget = v.maxHotBytes
	}
	if baseBudget <= 0 {
		return
	}

	idleBudget := baseBudget / 4
	if idleBudget < 1<<20 { // minimum 1 MiB
		idleBudget = 1 << 20
	}
	if idleBudget >= v.maxHotBytes {
		return
	}

	v.maxHotBytes = idleBudget
	v.markHotBudgetNeedsEnforcement()
	v.enforceHotBudgetCachedOnly()
}

// RestoreHotBudget restores the viewport hot budget to its baseline value.
func (v *Viewport) RestoreHotBudget() {
	if v == nil {
		return
	}
	baseBudget := v.baseHotBytes
	if baseBudget <= 0 {
		return
	}
	if v.maxHotBytes == baseBudget {
		return
	}
	v.maxHotBytes = baseBudget
	v.markHotBudgetNeedsEnforcement()
}
