package tui

import (
	"slices"

	"github.com/keakon/chord/internal/tools"
)

// ScrollDown moves the viewport down by n lines.
func (v *Viewport) ScrollDown(n int) {
	prevOffset := v.offset
	v.offset += n
	v.clampOffset()
	if v.offset != prevOffset {
		v.sticky = v.atBottom()
	}
}

// ScrollUp moves the viewport up by n lines.
func (v *Viewport) ScrollUp(n int) {
	v.offset -= n
	if v.offset < 0 {
		v.offset = 0
	}
	v.sticky = false
}

// ScrollToTop scrolls to the very beginning.
func (v *Viewport) ScrollToTop() {
	v.offset = 0
	v.sticky = false
}

// ScrollToBottom scrolls so the last line is at the bottom of the viewport.
func (v *Viewport) ScrollToBottom() {
	v.scrollToEnd()
	v.sticky = true
}

// NextMessageBoundary scrolls to the start of the next block.
func (v *Viewport) NextMessageBoundary() {
	starts := v.blockStarts()
	for _, s := range starts {
		if s > v.offset {
			v.offset = s
			v.clampOffset()
			v.sticky = v.atBottom()
			return
		}
	}
	v.ScrollToBottom()
}

// PrevMessageBoundary scrolls to the start of the previous block.
func (v *Viewport) PrevMessageBoundary() {
	starts := v.blockStarts()
	prev := 0
	for _, s := range starts {
		if s >= v.offset {
			break
		}
		prev = s
	}
	if prev < v.offset {
		v.offset = prev
	} else {
		v.offset = 0
	}
	v.sticky = false
}

// ToggleBlockAtOffset toggles the collapsed state of the block under the current scroll position.
func (v *Viewport) ToggleBlockAtOffset() {
	block := v.GetBlockAtOffset()
	if block == nil {
		return
	}
	oldStart, oldSpan, found := v.toggleAnchorPosition(block)
	if !block.ToggleAtWidth(v.width) {
		return
	}
	v.applyToggleOffsetAnchor(block, oldStart, oldSpan, found)
}

// ToggleBlockByID finds a block by its ID and toggles its collapsed state.
func (v *Viewport) ToggleBlockByID(id int) {
	for _, block := range v.blocks {
		if block.ID == id {
			block = v.materialize(block)
			oldStart, oldSpan, found := v.toggleAnchorPosition(block)
			if !block.ToggleAtWidth(v.width) {
				return
			}
			v.applyToggleOffsetAnchor(block, oldStart, oldSpan, found)
			v.enforceHotBudget()
			return
		}
	}
}

// toggleAnchorPosition captures the block's start line and rendered span while
// the block-position cache is still valid, i.e. before the toggle flips the
// collapsed state and invalidates it. found reports whether the block is part
// of the current visible (unfiltered) transcript.
func (v *Viewport) toggleAnchorPosition(block *Block) (start, span int, found bool) {
	start, found = v.LineOffsetForBlockID(block.ID)
	span = v.blockSpanLines(block)
	return start, span, found
}

// applyToggleOffsetAnchor repositions the viewport after a block has changed
// its rendered line count (collapse/expand) so the visible content stays put.
//
// The viewport offset is an absolute line index, but toggling a block changes
// only that block's span: everything after it shifts by the size delta while
// the offset itself stays, so the window drifts away from the content the user
// was reading and the card they were looking at seems to jump out of view. The
// anchor rules are:
//
//   - block starting inside the window: keep the offset, so the block's top
//     edge stays on the same screen row and the window grows/shrinks around it
//   - block extending into the window from above: keep the offset while the
//     card still occupies the window; if collapsing removes it from the window
//     entirely, scroll back so the collapsed card is visible again
//   - block entirely above the window: shift the offset by the span delta so
//     the visible lines do not move at all
//   - block entirely below the window: nothing on screen depends on its span
//   - sticky tail-following: keep the bottom anchored, but when the collapsed
//     card leaves the window, drop sticky and bring it back into view
func (v *Viewport) applyToggleOffsetAnchor(block *Block, oldStart, oldSpan int, found bool) {
	// Toggling changes the block's rendered line count and invalidates its
	// line/viewport caches. renderVersion must advance so the model-level
	// main-area cache key changes; otherwise the collapsed/expanded switch
	// is only picked up on the next scroll that changes the offset.
	v.bumpRenderVersion()
	v.markHotBudgetDirty()
	v.recalcTotalLines()

	if !found {
		// The block is not part of the visible transcript (e.g. hidden by an
		// agent filter): nothing on screen depends on its span.
		v.clampOffset()
		return
	}

	newStart, stillVisible := v.LineOffsetForBlockID(block.ID)
	if !stillVisible {
		// LineOffsetForBlockID reports absence as (0, false), so the bool is the
		// only failure signal: trusting the returned 0 would anchor the view to
		// the top of the transcript instead of to the toggled card.
		newStart = oldStart
	}
	newSpan := v.blockSpanLines(block)
	newEnd := newStart + newSpan
	oldEnd := oldStart + oldSpan
	delta := newSpan - oldSpan

	switch {
	case v.sticky:
		v.scrollToEnd()
		if newEnd <= v.offset {
			// The collapsed card left the window; bring it back so the user
			// can see the result of the toggle.
			v.sticky = false
			v.offset = newStart
			v.clampOffset()
		}
	case oldStart >= v.offset:
		// The block starts at or after the window's first line, either inside
		// the window or entirely below it. Its start line is unchanged by the
		// toggle, so keeping the offset keeps everything above it — and the
		// card's own top edge — on the same screen row.
		v.clampOffset()
	default:
		// The block extends into the window from above.
		switch {
		case oldEnd <= v.offset:
			// Block entirely above the window: shift the offset by the delta
			// so the visible content does not move.
			v.offset += delta
		case newEnd <= v.offset:
			// Collapsing removed the card from the window; scroll so its top
			// edge is back at the top of the window.
			v.offset = newStart
		default:
			// The card still occupies the window after the toggle; keep the
			// offset so the visible lines stay put.
		}
		v.clampOffset()
	}
}

// FocusedBlockIsVisible reports whether the block with the given ID falls
// inside the current scroll window. After a mouse-wheel scroll the focused
// block may still exist in the transcript while having scrolled out of view;
// key handlers that act on the focused block should fall back to the block at
// the current offset in that case instead of acting on an off-screen card.
func (v *Viewport) FocusedBlockIsVisible(id int) bool {
	if id < 0 || v == nil {
		return false
	}
	start, ok := v.LineOffsetForBlockID(id)
	if !ok {
		return false
	}
	block := v.blockForID(id)
	if block == nil {
		return false
	}
	end := start + v.blockSpanLines(v.materialize(block))
	return start < v.offset+v.height && end > v.offset
}

func (v *Viewport) blockForID(id int) *Block {
	for _, block := range v.blocks {
		if block != nil && block.ID == id {
			return block
		}
	}
	return nil
}

// GetBlockAtOffset returns the block that contains the current scroll offset line, or nil.
func (v *Viewport) GetBlockAtOffset() *Block {
	blocks := v.visibleBlocks()
	lineOffset := 0
	for i, block := range blocks {
		lc := v.blockSpanAt(blocks, i, block)
		if lineOffset+lc > v.offset {
			return v.materialize(block)
		}
		lineOffset += lc
	}
	return nil
}

// HasVisibleInlineImage reports whether any currently visible block contains at least one visible image.
func (v *Viewport) HasVisibleInlineImage() bool {
	if v == nil || v.height <= 0 {
		return false
	}
	blocks := v.visibleBlocks()
	starts := v.blockStarts()
	windowStart := v.offset
	windowEnd := v.offset + v.height
	for i, block := range blocks {
		if !blockSupportsImagePreview(block) {
			continue
		}
		if i >= len(starts) {
			break
		}
		blockStart := starts[i] + v.blockLeadingSpacing(blocks, i)
		for _, part := range block.ImageParts {
			if part.RenderRows <= 0 || part.RenderStartLine < 0 {
				continue
			}
			globalStart := blockStart + part.RenderStartLine
			globalEnd := blockStart + part.RenderEndLine
			if globalEnd >= windowStart && globalStart < windowEnd {
				return true
			}
		}
	}
	return false
}

// TotalLines returns the cached total number of rendered lines.
func (v *Viewport) TotalLines() int {
	return v.totalLines
}

func (v *Viewport) LineOffsetForBlockID(id int) (int, bool) {
	if id < 0 {
		return 0, false
	}
	blocks := v.visibleBlocks()
	starts := v.blockStarts()
	if len(blocks) == 0 || len(starts) != len(blocks) {
		return 0, false
	}
	for i, block := range blocks {
		if block != nil && block.ID == id {
			return starts[i], true
		}
	}
	return 0, false
}

func (v *Viewport) MessageDirectory() []DirectoryEntry {
	blocks := v.visibleBlocks()
	entries := make([]DirectoryEntry, 0, len(blocks))
	lineOffset := 0
	for i, block := range blocks {
		entries = append(entries, DirectoryEntry{
			BlockIndex: i,
			BlockID:    block.ID,
			LineOffset: lineOffset,
			Summary:    block.Summary(),
			Type:       block.Type,
		})
		lineOffset += v.blockSpanAt(blocks, i, block)
	}
	return entries
}

func (v *Viewport) FindBlockByToolID(toolID string) (*Block, bool) {
	for _, block := range v.blocks {
		if block.Type == BlockToolCall && block.ToolID == toolID {
			return v.materialize(block), true
		}
	}
	return nil, false
}

func (v *Viewport) FindLastPendingToolBlockByName(toolName string) (*Block, bool) {
	toolName = tools.NormalizeName(toolName)
	for _, b := range slices.Backward(v.blocks) {

		if b.Type == BlockToolCall && tools.NormalizeName(b.ToolName) == toolName && !b.ResultDone {
			return v.materialize(b), true
		}
	}
	return nil, false
}

// GetFocusedBlock returns the block with the given ID from all blocks (ignoring filter).
func (v *Viewport) GetFocusedBlock(id int) *Block {
	for _, block := range v.blocks {
		if block.ID == id {
			return v.materialize(block)
		}
	}
	return nil
}

// MaterializeBlockByID ensures the block with the given ID is hydrated and returns it.
func (v *Viewport) MaterializeBlockByID(id int) *Block {
	if v == nil {
		return nil
	}
	for _, block := range v.blocks {
		if block != nil && block.ID == id {
			return v.materialize(block)
		}
	}
	return nil
}

func (v *Viewport) FindStatusBlockByBackgroundObject(id string) (*Block, bool) {
	for _, block := range v.blocks {
		if block.Type == BlockStatus && block.BackgroundObjectID == id {
			return v.materialize(block), true
		}
	}
	return nil, false
}

func (v *Viewport) FindBlockByLinkedAgent(agentID string) (*Block, bool) {
	for _, block := range v.blocks {
		if block.Type == BlockToolCall && block.LinkedAgentID == agentID {
			return v.materialize(block), true
		}
	}
	return nil, false
}

func (v *Viewport) FindBlockByLinkedTask(taskID string) (*Block, bool) {
	for _, block := range v.blocks {
		if block.Type == BlockToolCall && block.LinkedTaskID == taskID {
			return v.materialize(block), true
		}
	}
	return nil, false
}
