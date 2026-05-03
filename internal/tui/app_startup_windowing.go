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
	m.recalcViewportSize()
	m.maybeEnforceStartupDeferredTranscriptRetention()
	state.windowStart = start
	state.windowEnd = end
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
		start := max(0, total-startupTranscriptTailBlocks)
		if !m.applyStartupDeferredTranscriptWindow(start, total, trigger) {
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

func (m *Model) maybeStepStartupDeferredTranscriptWindow(dir int, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 {
		return false
	}
	total := len(state.allBlocks)
	switch {
	case dir < 0:
		if state.windowStart <= 0 {
			return false
		}
		return m.maybeJumpDeferredStartupTranscriptOrdinal(state.windowStart, trigger)
	case dir > 0:
		if state.windowEnd >= total {
			return false
		}
		return m.maybeJumpDeferredStartupTranscriptOrdinal(state.windowEnd+1, trigger)
	default:
		return false
	}
}

func (m *Model) maybePageStartupDeferredTranscriptWindow(dir int, trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 {
		return false
	}
	total := len(state.allBlocks)
	switch {
	case dir < 0:
		if state.windowStart <= 0 {
			return false
		}
		target := state.windowStart - startupTranscriptTailBlocks + 1
		if target < 1 {
			target = 1
		}
		return m.maybeJumpDeferredStartupTranscriptOrdinal(target, trigger)
	case dir > 0:
		if state.windowEnd >= total {
			return false
		}
		target := state.windowEnd + startupTranscriptTailBlocks
		if target > total {
			target = total
		}
		return m.maybeJumpDeferredStartupTranscriptOrdinal(target, trigger)
	default:
		return false
	}
}

func (m *Model) maybeHydrateStartupDeferredTranscript(trigger string) bool {
	state := m.startupDeferredTranscript
	if state == nil || len(state.allBlocks) == 0 || m.viewport == nil {
		return false
	}
	started := time.Now()
	m.viewport.sticky = false
	m.viewport.ReplaceBlocks(state.allBlocks)
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
