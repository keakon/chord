package tui

import (
	"strings"
	"time"

	"github.com/keakon/golog/log"
)

const (
	startupTranscriptWindowMinBlocks = 96
	startupTranscriptTailBlocks      = 48
)

const (
	startupTranscriptWindowTop  = "top"
	startupTranscriptWindowTail = "tail"
)

func startupDeferredPageUpSwitchThreshold(viewportHeight int) int {
	if viewportHeight <= 0 {
		return 0
	}
	return max(0, viewportHeight/2)
}

type startupDeferredTranscriptState struct {
	allBlocks              []*Block
	blockMeta              []startupDeferredBlockMeta
	hiddenBlocks           int
	anchorBlockID          int
	windowStart            int
	windowEnd              int
	originalViewportBudget int64
	startedAt              time.Time
	windowSwitchCount      int
	preheatTickCount       int
	preheatBlocksProcessed int
}

func cloneBlocksForDeferredSource(blocks []*Block) []*Block {
	if len(blocks) == 0 {
		return nil
	}
	cloned := make([]*Block, 0, len(blocks))
	for _, block := range blocks {
		cloned = append(cloned, cloneBlockForDeferredSource(block))
	}
	return cloned
}

func (m *Model) startupDeferredWindowBlocks(state *startupDeferredTranscriptState, start, end int) []*Block {
	if state == nil || len(state.allBlocks) == 0 {
		return nil
	}
	if start < 0 {
		start = 0
	}
	if end > len(state.allBlocks) {
		end = len(state.allBlocks)
	}
	if start >= end {
		return nil
	}
	return state.allBlocks[start:end]
}

func startupDeferredWindowRange(total, targetIndex int) (start, end int) {
	windowCount := startupTranscriptTailBlocks
	if total <= windowCount {
		return 0, total
	}
	if targetIndex < 0 {
		targetIndex = 0
	}
	if targetIndex >= total {
		targetIndex = total - 1
	}
	half := windowCount / 2
	start = targetIndex - half
	if start < 0 {
		start = 0
	}
	end = start + windowCount
	if end > total {
		end = total
		start = end - windowCount
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func startupDeferredPageWindowRange(total, start, dir int) (nextStart, nextEnd int, ok bool) {
	windowCount := startupTranscriptTailBlocks
	if total <= 0 || windowCount <= 0 {
		return 0, 0, false
	}
	if total <= windowCount {
		return 0, total, false
	}
	if start < 0 {
		start = 0
	}
	if start > total-windowCount {
		start = max(0, total-windowCount)
	}
	switch {
	case dir < 0:
		if start <= 0 {
			return 0, 0, false
		}
		nextStart = max(0, start-windowCount)
		nextEnd = nextStart + windowCount
		if nextEnd > total {
			nextEnd = total
		}
		return nextStart, nextEnd, true
	case dir > 0:
		if start+windowCount >= total {
			return 0, 0, false
		}
		nextStart = start + windowCount
		if nextStart > total-windowCount {
			nextStart = max(0, total-windowCount)
		}
		nextEnd = nextStart + windowCount
		if nextEnd > total {
			nextEnd = total
		}
		return nextStart, nextEnd, true
	default:
		return 0, 0, false
	}
}

func (m *Model) applyStartupDeferredTranscriptWindow(start, end int, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	if start == state.windowStart && end == state.windowEnd {
		return false
	}
	started := time.Now()
	m.configureStartupDeferredTranscriptRetention(state)
	windowed := m.startupDeferredWindowBlocks(state, start, end)
	if len(windowed) == 0 {
		return false
	}
	m.viewport.sticky = false
	m.viewport.ReplaceBlocks(windowed)
	m.rebindLiveViewportBlocks()
	m.recalcViewportSize()
	m.maybeEnforceStartupDeferredTranscriptRetention()
	state.windowStart = start
	state.windowEnd = end
	for _, block := range windowed {
		if block != nil {
			state.anchorBlockID = block.ID
			break
		}
	}
	state.windowSwitchCount++
	m.restartStartupDeferredTranscriptPreheat(startupDeferredTranscriptPreheatDelay)
	hiddenBefore := start
	hiddenAfter := len(state.allBlocks) - end
	log.Debugf("tui startup transcript window switch trigger=%v window_start=%v window_end=%v window_blocks=%v hidden_before=%v hidden_after=%v total_ms=%v", strings.TrimSpace(trigger), start, end, len(windowed), hiddenBefore, hiddenAfter, time.Since(started).Milliseconds())
	return true
}

func (m *Model) maybeWindowStartupTranscript(reason string, blocks []*Block) []*Block {
	if reason != "startup_restored" {
		m.startupDeferredTranscript = nil
		m.startupDeferredPreheatGeneration++
		return blocks
	}
	if len(blocks) <= startupTranscriptWindowMinBlocks {
		m.startupDeferredTranscript = nil
		m.startupDeferredPreheatGeneration++
		return blocks
	}
	tailCount := startupTranscriptTailBlocks
	if tailCount >= len(blocks) {
		m.startupDeferredTranscript = nil
		m.startupDeferredPreheatGeneration++
		return blocks
	}
	hiddenCount := len(blocks) - tailCount
	anchor := blocks[hiddenCount]
	anchorID := -1
	if anchor != nil {
		anchorID = anchor.ID
	}
	state := &startupDeferredTranscriptState{
		allBlocks:              cloneBlocksForDeferredSource(blocks),
		blockMeta:              buildStartupDeferredBlockMeta(blocks, m.viewport.width),
		hiddenBlocks:           hiddenCount,
		anchorBlockID:          anchorID,
		windowStart:            hiddenCount,
		windowEnd:              len(blocks),
		originalViewportBudget: 0,
		startedAt:              time.Now(),
	}
	m.startupDeferredTranscript = state
	m.configureStartupDeferredTranscriptRetention(state)
	windowed := m.startupDeferredWindowBlocks(state, state.windowStart, state.windowEnd)
	for _, block := range windowed {
		if block != nil {
			block.InvalidateCache()
		}
	}
	log.Debugf("tui startup transcript windowed blocks=%v hidden_blocks=%v window_blocks=%v", len(blocks), hiddenCount, len(windowed))
	m.logStartupDeferredTranscriptRetention(state, len(blocks))
	return windowed
}

func (m *Model) hasDeferredStartupTranscript() bool {
	return m.startupDeferredTranscript != nil && len(m.startupDeferredTranscript.allBlocks) > 0
}

func (m *Model) maybeSwitchStartupDeferredTranscriptWindow(targetWindow, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	total := len(state.allBlocks)
	switch strings.TrimSpace(targetWindow) {
	case startupTranscriptWindowTop:
		if !m.applyStartupDeferredTranscriptWindow(0, min(total, startupTranscriptTailBlocks), trigger) {
			return false
		}
		m.viewport.offset = 0
		m.viewport.sticky = false
		return true
	case startupTranscriptWindowTail:
		windowSize := state.windowEnd - state.windowStart
		if windowSize <= 0 {
			windowSize = startupTranscriptTailBlocks
		}
		if windowSize > total {
			windowSize = total
		}
		start := max(0, total-windowSize)
		windowChanged := m.applyStartupDeferredTranscriptWindow(start, total, trigger)
		// Even if the window boundaries didn't change, we still need to ensure
		// the viewport is scrolled to the bottom when explicitly requesting tail.
		// This handles cases where content arrived during blur or the viewport
		// offset drifted while the window state remained at tail.
		if !windowChanged && (state.windowStart != start || state.windowEnd != total) {
			return false
		}
		m.viewport.ScrollToBottom()
		m.viewport.sticky = true
		return true
	default:
		return false
	}
}

func (m *Model) maybeJumpDeferredStartupTranscriptOrdinal(ordinal int, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	total := len(state.allBlocks)
	if ordinal < 1 {
		ordinal = 1
	}
	if ordinal > total {
		ordinal = total
	}
	targetIndex := ordinal - 1
	start, end := startupDeferredWindowRange(total, targetIndex)
	m.applyStartupDeferredTranscriptWindow(start, end, trigger)
	targetID := state.allBlocks[targetIndex].ID
	if target := m.viewport.GetFocusedBlock(targetID); target != nil && isSelectableBlockType(target.Type) {
		m.focusedBlockID = targetID
	} else {
		m.focusedBlockID = -1
	}
	m.refreshBlockFocus()
	if lineOffset, ok := m.viewport.LineOffsetForBlockID(targetID); ok {
		m.viewport.offset = lineOffset
		m.viewport.clampOffset()
	}
	m.viewport.sticky = m.viewport.atBottom()
	return true
}

func (m *Model) deferredSelectableBlockIndex(id int) int {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 {
		return -1
	}
	for i, block := range state.allBlocks {
		if block == nil || block.ID != id || !isSelectableBlockType(block.Type) {
			continue
		}
		return i
	}
	return -1
}

func (m *Model) deferredCurrentSelectableBlockIndex() int {
	if m == nil || m.viewport == nil {
		return -1
	}
	if m.focusedBlockID >= 0 {
		if idx := m.deferredSelectableBlockIndex(m.focusedBlockID); idx >= 0 {
			return idx
		}
	}
	block := m.viewport.GetBlockAtOffset()
	if block == nil {
		return -1
	}
	return m.deferredSelectableBlockIndex(block.ID)
}

func (m *Model) deferredSeekSelectableBlock(startIndex, dir int) int {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 {
		return -1
	}
	if dir == 0 {
		dir = 1
	}
	for i := startIndex; i >= 0 && i < len(state.allBlocks); i += dir {
		block := state.allBlocks[i]
		if block == nil || !isSelectableBlockType(block.Type) {
			continue
		}
		return i
	}
	return -1
}

func (m *Model) deferredMoveFocusedBlock(dir int) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil || dir == 0 {
		return false
	}
	currentIndex := m.deferredCurrentSelectableBlockIndex()
	startIndex := 0
	if dir < 0 {
		startIndex = len(state.allBlocks) - 1
	}
	if currentIndex >= 0 {
		startIndex = currentIndex + dir
	}
	targetIndex := m.deferredSeekSelectableBlock(startIndex, dir)
	if targetIndex < 0 {
		if dir < 0 && m.maybeHydrateStartupDeferredTranscript("count_boundary_top") {
			firstIndex := m.deferredSeekSelectableBlock(0, 1)
			if firstIndex >= 0 {
				targetID := state.allBlocks[firstIndex].ID
				m.focusedBlockID = targetID
				m.refreshBlockFocus()
				if lineOffset, ok := m.viewport.LineOffsetForBlockID(targetID); ok {
					m.viewport.offset = lineOffset
					m.viewport.clampOffset()
				}
			}
		}
		return false
	}
	start, end := startupDeferredWindowRange(len(state.allBlocks), targetIndex)
	m.applyStartupDeferredTranscriptWindow(start, end, "count_boundary")
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

func (m *Model) deferredScrollOneLine(dir int, trigger string) bool {
	if m == nil || m.viewport == nil || dir == 0 || !m.hasDeferredStartupTranscript() {
		return false
	}
	if dir < 0 {
		if m.viewport.offset <= startupDeferredPageUpSwitchThreshold(m.viewport.height) {
			if m.maybeStepStartupDeferredTranscriptWindow(-1, trigger) {
				return true
			}
			if m.maybeHydrateStartupDeferredTranscript(trigger) {
				return true
			}
		}
		m.viewport.ScrollUp(1)
		return true
	}
	if m.viewport.atBottom() {
		if m.startupDeferredTranscriptAtTail() {
			return true
		}
		if m.maybeStepStartupDeferredTranscriptWindow(1, trigger) {
			return true
		}
	}
	m.viewport.ScrollDown(1)
	return true
}

func (m *Model) maybeStepStartupDeferredTranscriptWindow(dir int, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	start, end, ok := startupDeferredPageWindowRange(len(state.allBlocks), state.windowStart, dir)
	if !ok {
		return false
	}
	if !m.applyStartupDeferredTranscriptWindow(start, end, trigger) {
		return false
	}
	switch {
	case dir < 0:
		m.viewport.ScrollToBottom()
		m.viewport.sticky = false
	case dir > 0:
		m.viewport.offset = 0
		m.viewport.clampOffset()
		m.viewport.sticky = m.viewport.atBottom()
	}
	m.focusedBlockID = -1
	m.refreshBlockFocus()
	return true
}

func (m *Model) maybePageStartupDeferredTranscriptWindow(dir int, trigger string) bool {
	return m.maybeStepStartupDeferredTranscriptWindow(dir, trigger)
}

func (m *Model) maybeHydrateStartupDeferredTranscript(trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	started := time.Now()
	m.viewport.sticky = false
	m.viewport.ReplaceBlocks(state.allBlocks)
	m.rebindLiveViewportBlocks()
	m.restoreStartupDeferredTranscriptRetention(state)
	if state.anchorBlockID >= 0 {
		if lineOffset, ok := m.viewport.LineOffsetForBlockID(state.anchorBlockID); ok {
			m.viewport.offset = lineOffset
			m.viewport.clampOffset()
		}
	}
	m.recalcViewportSize()
	m.logStartupDeferredTranscriptExit(state, "hydrate", trigger)
	m.startupDeferredTranscript = nil
	m.startupDeferredPreheatGeneration++
	m.viewport.enforceHotBudget()
	log.Debugf("tui startup transcript hydrate timing trigger=%v blocks=%v hidden_blocks=%v total_ms=%v", strings.TrimSpace(trigger), len(state.allBlocks), state.hiddenBlocks, time.Since(started).Milliseconds())
	return true
}
