package tui

// ScrollDown moves the viewport down by n lines.
func (v *Viewport) ScrollDown(n int) {
	v.offset += n
	v.clampOffset()
	v.sticky = v.atBottom()
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
	if block != nil {
		block.ToggleAtWidth(v.width)
		v.markHotBudgetDirty()
		v.recalcTotalLines()
		v.clampOffset()
	}
}

// ToggleBlockByID finds a block by its ID and toggles its collapsed state.
func (v *Viewport) ToggleBlockByID(id int) {
	for _, block := range v.blocks {
		if block.ID == id {
			block = v.materialize(block)
			block.ToggleAtWidth(v.width)
			v.markHotBudgetDirty()
			v.recalcTotalLines()
			v.clampOffset()
			v.enforceHotBudget()
			return
		}
	}
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
		if block == nil || block.Type != BlockUser || len(block.ImageParts) == 0 {
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
	for i := len(v.blocks) - 1; i >= 0; i-- {
		b := v.blocks[i]
		if b.Type == BlockToolCall && b.ToolName == toolName && b.ResultContent == "" {
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
