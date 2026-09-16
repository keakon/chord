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

// DropOffScreenCaches reclaims the render caches of blocks that are not
// currently visible in the viewport window. It preserves:
//   - The currently visible window and its immediate neighbors
//   - Blocks that are still streaming or pending
//   - The focused block
//
// This is a lightweight operation: it only clears cache references, it does
// not re-render anything or perform spill-store I/O. It is called from the
// background idle sweep, so it also drops the derived caches (rendered
// Markdown, highlighters) that a sweep on the visible foreground path would
// keep for instant scroll-back.
func (v *Viewport) DropOffScreenCaches() {
	if v == nil {
		return
	}

	margin := 5 // rows above/below visible window to preserve
	visibleStart := max(v.offset-margin, 0)
	visibleEnd := v.offset + v.height + margin

	currentLine := 0
	blocks := v.visibleBlocks()
	for i, block := range blocks {
		blockLines, ok := v.offscreenSweepSpan(blocks, i, block)
		if !ok {
			return
		}
		blockStart := currentLine
		blockEnd := currentLine + blockLines

		if blockEnd < visibleStart || blockStart > visibleEnd {
			if v.canDropBlockCache(block) {
				block.ReleaseDerivedRenderCaches()
			}
		}

		currentLine = blockEnd
	}
}

// offscreenSweepSpan returns the line span the viewport currently assigns to a
// block. The viewport's own position cache is authoritative while it is valid,
// so a block whose per-block line cache was already released no longer stops
// the sweep and leaves every later off-screen block uncleared. Without a valid
// position cache it falls back to the per-block cache; a block with neither
// still stops the sweep conservatively, so later offsets are never computed
// from stale line counts.
func (v *Viewport) offscreenSweepSpan(blocks []*Block, index int, block *Block) (int, bool) {
	if v.blockPositionCacheValid(blocks) && index < len(v.blockSpansCache) {
		if span := v.blockSpansCache[index]; span > 0 {
			return span, true
		}
	}
	return v.cachedLineCount(block, v.width)
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

	idleBudget := max(baseBudget/4,
		// minimum 1 MiB
		1<<20)
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
