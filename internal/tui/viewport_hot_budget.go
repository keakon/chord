package tui

func (v *Viewport) enforceHotBudget() {
	if v.maxHotBytes <= 0 {
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
	v.enforceHotBudgetFor(visible, false)
}

// enforceHotBudgetFor reclaims memory in two levels: first it drops the
// derived render caches of off-screen blocks (no spill-store I/O, content
// stays resident), and only if that is not enough does it spill whole blocks to
// disk. The cheaper level runs first because a re-render on scroll-back is
// much cheaper than a spill-store read plus decode plus re-render.
//
// allowFocusedSpill is set by the quiescent enforcement paths (idle shrink and
// startup preheat), where a focused card that is out of view may go cold and
// hydrate on the next interaction. The active path keeps focused cards hot.
func (v *Viewport) enforceHotBudgetFor(visible map[int]struct{}, allowFocusedSpill bool) {
	if v.hotBytes <= v.maxHotBytes {
		return
	}
	v.releaseOffScreenRenderCaches(visible)
	if v.spill == nil {
		return
	}
	for v.hotBytes > v.maxHotBytes {
		var candidate *Block
		for _, block := range v.blocks {
			if !v.hotBudgetSpillable(block, visible, allowFocusedSpill) {
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

// hotBudgetCacheReleasable reports whether the hot budget may drop a block's
// derived render caches: it must be off-screen and not owned by active work.
// The spill-exempt large-block check is deliberately absent — such blocks still
// release their caches, they only avoid the disk round-trip.
func (v *Viewport) hotBudgetCacheReleasable(block *Block, visible map[int]struct{}) bool {
	if block == nil || block.spillCold {
		return false
	}
	if _, ok := visible[block.ID]; ok {
		return false
	}
	return v.canDropBlockCache(block)
}

// hotBudgetSpillable reports whether the hot budget may spill a block to disk.
// Streaming and pending work are never spillable: their content is still being
// appended. Focused cards are spilled only by the quiescent paths.
func (v *Viewport) hotBudgetSpillable(block *Block, visible map[int]struct{}, allowFocused bool) bool {
	if block == nil || block.spillCold {
		return false
	}
	if _, ok := visible[block.ID]; ok {
		return false
	}
	if block.Streaming || block.UserLocalShellPending {
		return false
	}
	if block.Type == BlockToolCall && !block.ResultDone {
		return false
	}
	if block.Focused && !allowFocused {
		return false
	}
	return true
}

// releaseOffScreenRenderCaches drops rebuildable caches (Markdown body,
// highlighters, search and rendered-line caches) from off-screen blocks until
// the budget is met. Content and line-count metadata stay, so block offsets
// remain valid and scroll-back only pays one re-render.
//
// Blocks are visited in transcript order, oldest first. That is a close
// approximation of LRU and keeps enforcement O(blocks) without sorting on every
// append, which matters when a large transcript is still being built.
func (v *Viewport) releaseOffScreenRenderCaches(visible map[int]struct{}) {
	for _, block := range v.blocks {
		if v.hotBytes <= v.maxHotBytes {
			return
		}
		if !v.hotBudgetCacheReleasable(block, visible) {
			continue
		}
		block.hotBytesMemoValid = false
		before := block.estimatedHotBytes()
		block.ReleaseDerivedRenderCaches()
		after := block.estimatedHotBytes()
		if after >= before {
			continue
		}
		v.hotBytes -= before - after
		if v.hotBytes < 0 {
			v.hotBytes = 0
		}
	}
}

// blockTooLargeToSpill reports whether a block is too large to spill to disk.
// Materializing such a block from the spill store on scroll-back costs a full
// disk read, JSON decode, and whole-block re-render (hundreds of ms for a
// multi-hundred-KB card), so keeping its content resident avoids a visible
// scroll hitch even though it consumes more hot-budget memory. The block still
// releases its derived render caches first; only the spill round-trip is
// skipped.
//
// The spill exemption is unconditional, so hot bytes can settle above
// maxHotBytes: enforceHotBudget stops when every remaining candidate is exempt.
// The excess is bounded by how many such blocks a session actually produces,
// not by the budget, and the gap is widest under the idle budget (a quarter of
// the baseline, floor 1 MiB), where one exempt block can exceed the whole
// budget.
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

// ResidentBytes reports the estimated bytes every block still holds, whether or
// not spill can reclaim them. It is a diagnostic counterpart to hotBytes, not a
// budget input: hotBytes decides what to spill, this says what is actually kept.
func (v *Viewport) ResidentBytes() int64 {
	if v == nil {
		return 0
	}
	var total int64
	for _, block := range v.blocks {
		total += block.residentBytes()
	}
	return total
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
	if v.maxHotBytes <= 0 {
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
	v.enforceHotBudgetFor(visible, true)
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
