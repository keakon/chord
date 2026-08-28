package tui

import tea "github.com/keakon/bubbletea/v2"

// blockTypeMatch returns a jump predicate that accepts selectable cards of the
// given type. The intersection with isSelectableBlockType is deliberate: jump
// keys must never land on unselectable cards such as BlockError, matching the
// existing j/k boundary constraint.
func blockTypeMatch(t BlockType) func(*Block) bool {
	return func(b *Block) bool {
		return b != nil && isSelectableBlockType(b.Type) && b.Type == t
	}
}

// userTurnBlockMatch accepts user message cards that begin an agent turn.
// Local !shell cards are also BlockUser but execute locally and never start a
// turn, so the turn-boundary jump keys ({ / }) must skip them.
func userTurnBlockMatch(b *Block) bool {
	return b != nil && isSelectableBlockType(b.Type) && b.Type == BlockUser && !b.IsUserLocalShell()
}

// nthMatchingBlockIndex scans once from startIndex and returns the count-th
// matching block, or the last match before the boundary when fewer remain.
// This gives counted structural jumps saturating semantics without repeatedly
// rescanning the transcript or rebuilding deferred windows.
func nthMatchingBlockIndex(blocks []*Block, startIndex, dir, count int, match func(*Block) bool) int {
	if dir == 0 {
		dir = 1
	}
	if count < 1 {
		count = 1
	}
	targetIndex := -1
	for i := startIndex; i >= 0 && i < len(blocks); i += dir {
		if !match(blocks[i]) {
			continue
		}
		targetIndex = i
		count--
		if count == 0 {
			break
		}
	}
	return targetIndex
}

// sameTypeJumpMatch resolves the template type for [ / ] jumps: the focused
// card when one can be resolved, otherwise the card at the viewport offset.
// The second return value is false when no template card can be resolved.
func (m *Model) sameTypeJumpMatch() (func(*Block) bool, bool) {
	id := m.currentBlockID()
	if id < 0 {
		return nil, false
	}
	b := m.viewport.GetFocusedBlock(id)
	if b == nil {
		// The focused card may live in the deferred allBlocks transcript but
		// outside the visible window.
		b = m.startupDeferredBlockByID(id)
	}
	if b == nil {
		return nil, false
	}
	return blockTypeMatch(b.Type), true
}

// jumpToMatchingBlock moves focus to the next (dir > 0) or previous (dir < 0)
// card satisfying match, repeated count times, and scrolls the viewport so the
// landed card is visible. When no matching card exists in that direction the
// focus and offset stay untouched, matching existing boundary behavior.
func (m *Model) jumpToMatchingBlock(dir, count int, match func(*Block) bool) tea.Cmd {
	if count < 1 {
		count = 1
	}
	prevOffset := m.viewport.offset
	if m.hasDeferredStartupTranscript() {
		m.deferredJumpToMatchingBlock(dir, count, match)
		return m.refreshInlineImagesIfViewportMoved(prevOffset)
	}
	blocks := m.viewport.visibleBlocks()
	if len(blocks) == 0 {
		return nil
	}
	currentID := m.currentBlockID()
	if currentID < 0 {
		return nil
	}
	currentIndex := indexOfBlockID(blocks, currentID)
	startIndex := 0
	if dir < 0 {
		startIndex = len(blocks) - 1
	}
	if currentIndex >= 0 {
		startIndex = currentIndex + dir
	}
	targetIndex := nthMatchingBlockIndex(blocks, startIndex, dir, count, match)
	if targetIndex < 0 {
		return nil
	}
	targetID := blocks[targetIndex].ID
	m.focusedBlockID = targetID
	m.refreshBlockFocus()
	// Position the viewport via the cached block starts; building a
	// MessageDirectory here would run a full per-block Summary() pass on every
	// structural jump.
	if lineOffset, ok := m.viewport.LineOffsetForBlockID(targetID); ok {
		m.viewport.offset = lineOffset
		m.viewport.clampOffset()
		m.viewport.sticky = m.viewport.atBottom()
	}
	return m.refreshInlineImagesIfViewportMoved(prevOffset)
}

// deferredCurrentBlockIndex returns the index of the current card in the
// deferred allBlocks: the focused block when one exists, otherwise the block
// at the viewport offset. Unlike deferredCurrentSelectableBlockIndex it does
// not require the card to be selectable, so jump predicates decide matching.
func (m *Model) deferredCurrentBlockIndex() int {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return -1
	}
	if m.focusedBlockID >= 0 {
		if idx := indexOfBlockID(state.allBlocks, m.focusedBlockID); idx >= 0 {
			return idx
		}
	}
	block := m.viewport.GetBlockAtOffset()
	if block == nil {
		return -1
	}
	return indexOfBlockID(state.allBlocks, block.ID)
}

// deferredJumpToMatchingBlock is the deferred-windowing variant of
// jumpToMatchingBlock: it seeks in allBlocks, switches the visible window
// around the landed card, and positions the viewport. A miss stays put —
// the seek already covers the full transcript, so hydrating cannot reveal a
// new match and would only reset the offset to the window anchor.
func (m *Model) deferredJumpToMatchingBlock(dir, count int, match func(*Block) bool) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil || dir == 0 {
		return false
	}
	startIndex := 0
	if dir < 0 {
		startIndex = len(state.allBlocks) - 1
	}
	if current := m.deferredCurrentBlockIndex(); current >= 0 {
		startIndex = current + dir
	}
	targetIndex := nthMatchingBlockIndex(state.allBlocks, startIndex, dir, count, match)
	if targetIndex < 0 {
		// No matching card in this direction across the whole transcript:
		// keep focus and offset untouched, mirroring the non-deferred boundary
		// no-op. Hydrating would also move the viewport to the window anchor
		// instead of the last successful landing card on an out-of-range count.
		return false
	}
	start, end := startupDeferredWindowRange(len(state.allBlocks), targetIndex)
	m.applyStartupDeferredTranscriptWindow(start, end, "type_jump")
	targetID := state.allBlocks[targetIndex].ID
	m.focusedBlockID = targetID
	m.refreshBlockFocus()
	if lineOffset, ok := m.viewport.LineOffsetForBlockID(targetID); ok {
		m.viewport.offset = lineOffset
		m.viewport.clampOffset()
	}
	m.viewport.sticky = m.startupDeferredTranscriptAtTail() && m.viewport.atBottom()
	return true
}
