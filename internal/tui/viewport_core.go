package tui

import "time"

// DirectoryEntry is one row in the message-directory overlay (Ctrl+J).
type DirectoryEntry struct {
	BlockIndex int
	BlockID    int
	LineOffset int
	Summary    string
	Type       BlockType
}

// Viewport is a virtual-scrolling container that renders Block elements on
// demand. Only the lines visible in [offset, offset+height) are materialised.
type Viewport struct {
	blocks         []*Block
	offset         int
	height         int
	width          int
	sticky         bool
	totalLines     int
	lastBlockSpan  int
	filterAgentID  string
	spill          *ViewportSpillStore
	maxHotBytes    int64
	baseHotBytes   int64
	hotBytes       int64
	accessSeq      uint64
	spillRecovery  func() []*Block
	lastBlockDirty bool
	workingDir     string

	visibleBlocksCache []*Block
	visibleBlocksDirty bool
	hotBudgetDirty     bool
	hotBytesDirty      bool
	blockStartsCache   []int
	blockSpansCache    []int
	renderVersion      uint64
}

func NewViewport(width, height int) *Viewport {
	spill, _ := newViewportSpillStore()
	return &Viewport{
		width:              width,
		height:             height,
		sticky:             true,
		spill:              spill,
		maxHotBytes:        defaultViewportHotBytes,
		baseHotBytes:       defaultViewportHotBytes,
		visibleBlocksDirty: true,
		hotBudgetDirty:     true,
		hotBytesDirty:      true,
	}
}

func (v *Viewport) SetWorkingDir(path string) {
	if v == nil {
		return
	}
	v.workingDir = path
	for _, block := range v.blocks {
		if block == nil {
			continue
		}
		block.displayWorkingDir = path
		block.InvalidateCache()
	}
	v.bumpRenderVersion()
	v.recalcTotalLines()
}

func (v *Viewport) HasUserLocalShellPending() bool {
	for _, b := range v.visibleBlocks() {
		if b.Type == BlockUser && b.UserLocalShellCmd != "" && b.UserLocalShellPending {
			return true
		}
	}
	return false
}

func (v *Viewport) LatestVisiblePendingUserLocalShellStartedAt() (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	var latest time.Time
	for _, b := range v.visibleBlocks() {
		if b != nil && b.Type == BlockUser && b.UserLocalShellCmd != "" && b.UserLocalShellPending && !b.StartedAt.IsZero() && b.StartedAt.After(latest) {
			latest = b.StartedAt
		}
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	return latest, true
}

func (v *Viewport) HasPendingToolWork() bool {
	for _, b := range v.visibleBlocks() {
		if b != nil && b.Type == BlockToolCall && !b.ResultDone {
			return true
		}
	}
	return false
}

func (v *Viewport) AppendBlock(block *Block) {
	v.prepareBlock(block)
	v.blocks = append(v.blocks, block)
	v.invalidateVisibleBlocksCache()
	v.markHotBudgetDirty()
	v.bumpRenderVersion()
	v.recalcTotalLines()
	if v.sticky {
		v.scrollToEnd()
	}
	v.enforceHotBudget()
}

func (v *Viewport) AppendBlocks(blocks []*Block) {
	for _, b := range blocks {
		v.prepareBlock(b)
	}
	v.blocks = append(v.blocks, blocks...)
	v.invalidateVisibleBlocksCache()
	v.markHotBudgetDirty()
	v.bumpRenderVersion()
	v.recalcTotalLines()
	if v.sticky {
		v.scrollToEnd()
	}
	v.enforceHotBudget()
}

func (v *Viewport) HasBlocksForAgent(agentID string) bool {
	if agentID == "main" {
		agentID = ""
	}
	for _, b := range v.blocks {
		if b.AgentID == agentID {
			return true
		}
	}
	return false
}

func (v *Viewport) RemoveLastUserBlock() {
	for i := len(v.blocks) - 1; i >= 0; i-- {
		if v.blocks[i].Type == BlockUser {
			v.blocks = append(v.blocks[:i], v.blocks[i+1:]...)
			v.invalidateVisibleBlocksCache()
			v.markHotBudgetDirty()
			v.bumpRenderVersion()
			v.recalcTotalLines()
			return
		}
	}
}

func (v *Viewport) RemoveBlockByID(id int) {
	for i := len(v.blocks) - 1; i >= 0; i-- {
		if v.blocks[i].ID == id {
			v.blocks = append(v.blocks[:i], v.blocks[i+1:]...)
			v.invalidateVisibleBlocksCache()
			v.markHotBudgetDirty()
			v.bumpRenderVersion()
			v.recalcTotalLines()
			if v.sticky {
				v.scrollToEnd()
			}
			return
		}
	}
}

func (v *Viewport) InsertBlockBefore(beforeID int, newBlock *Block) {
	v.prepareBlock(newBlock)
	for i, b := range v.blocks {
		if b.ID == beforeID {
			v.blocks = append(v.blocks[:i], append([]*Block{newBlock}, v.blocks[i:]...)...)
			v.invalidateVisibleBlocksCache()
			v.markHotBudgetDirty()
			v.bumpRenderVersion()
			v.recalcTotalLines()
			if v.sticky {
				v.scrollToEnd()
			}
			v.enforceHotBudget()
			return
		}
	}
	v.AppendBlock(newBlock)
}

func (v *Viewport) ReplaceBlocks(blocks []*Block) {
	v.blocks = make([]*Block, len(blocks))
	copy(v.blocks, blocks)
	v.invalidateVisibleBlocksCache()
	v.markHotBudgetDirty()
	v.bumpRenderVersion()
	v.hotBytes = 0
	for _, b := range v.blocks {
		v.prepareBlock(b)
		b.viewportCache = nil
		b.viewportCacheWidth = 0
	}
	v.recalcTotalLines()
	v.clampOffset()
	if v.sticky {
		v.scrollToEnd()
	}
	v.enforceHotBudget()
}

func (v *Viewport) InvalidateLastBlock() {
	v.lastBlockDirty = true
	v.bumpRenderVersion()
}

func (v *Viewport) UpdateLastBlock() {
	v.lastBlockDirty = false
	blocks := v.visibleBlocks()
	if len(blocks) == 0 {
		return
	}
	v.bumpRenderVersion()
	lastIdx := len(blocks) - 1
	oldSpan := v.lastBlockSpan
	newSpan := v.measuredBlockSpanAt(blocks, lastIdx, blocks[lastIdx])
	v.totalLines += newSpan - oldSpan
	v.lastBlockSpan = newSpan
	v.invalidateCachesForLineCountChange()
	if v.sticky {
		v.scrollToEnd()
	} else {
		v.clampOffset()
	}
}

func (v *Viewport) UpdateBlock(id int) {
	blocks := v.visibleBlocks()
	if len(blocks) == 0 {
		return
	}
	v.bumpRenderVersion()
	last := blocks[len(blocks)-1]
	if last != nil && last.ID == id {
		v.markHotBudgetDirty()
		v.UpdateLastBlock()
		return
	}
	v.markHotBudgetDirty()
	v.recalcTotalLines()
	if v.sticky {
		v.scrollToEnd()
	} else {
		v.clampOffset()
	}
}

func (v *Viewport) clampOffset() {
	maxOffset := v.totalLines - v.height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if v.offset > maxOffset {
		v.offset = maxOffset
	}
	if v.offset < 0 {
		v.offset = 0
	}
}

func (v *Viewport) scrollToEnd() {
	v.offset = v.totalLines - v.height
	if v.offset < 0 {
		v.offset = 0
	}
}

func (v *Viewport) atBottom() bool {
	return v.offset >= v.totalLines-v.height
}
