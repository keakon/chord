package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const startupDeferredTranscriptPreheatDelay = 250 * time.Millisecond
const startupDeferredTranscriptPreheatHalo = 10

type startupDeferredPreheatTickMsg struct {
	generation uint64
}

func startupDeferredPreheatTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = startupDeferredTranscriptPreheatDelay
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return startupDeferredPreheatTickMsg{generation: generation}
	})
}

func (m *Model) scheduleStartupDeferredTranscriptPreheat(delay time.Duration) tea.Cmd {
	if m.startupDeferredTranscript == nil {
		return nil
	}
	m.startupDeferredPreheatGeneration++
	return startupDeferredPreheatTick(m.startupDeferredPreheatGeneration, delay)
}

func (m *Model) restartStartupDeferredTranscriptPreheat(delay time.Duration) tea.Cmd {
	if m.startupDeferredTranscript == nil {
		return nil
	}
	m.startupDeferredPreheatGeneration++
	return startupDeferredPreheatTick(m.startupDeferredPreheatGeneration, delay)
}

func (m *Model) handleStartupDeferredTranscriptPreheat(msg startupDeferredPreheatTickMsg) tea.Cmd {
	if msg.generation != m.startupDeferredPreheatGeneration {
		return nil
	}
	state := m.startupDeferredTranscript
	if state == nil || m.viewport == nil || m.isAgentBusy() {
		return nil
	}
	if state.windowStart <= 0 && state.windowEnd >= len(state.allBlocks) {
		return nil
	}
	if m.mode == ModeSearch || m.mode == ModeDirectory || m.mode == ModeQuestion || m.mode == ModeConfirm || m.mode == ModeSessionDeleteConfirm {
		return nil
	}

	preheated := m.preheatStartupDeferredTranscriptNeighbors(state)
	state.preheatTickCount++
	if preheated {
		m.viewport.markHotBudgetNeedsEnforcement()
		m.viewport.enforceHotBudgetCachedOnly()
	}
	if m.startupDeferredTranscript == nil {
		return nil
	}
	return m.scheduleStartupDeferredTranscriptPreheat(startupDeferredTranscriptPreheatDelay)
}

func (m *Model) preheatStartupDeferredTranscriptNeighbors(state *startupDeferredTranscriptState) bool {
	if state == nil || m.viewport == nil || len(state.allBlocks) == 0 {
		return false
	}
	width := m.viewport.width
	if width <= 0 {
		width = 80
	}
	preheated := false
	leftStart := max(0, state.windowStart-startupDeferredTranscriptPreheatHalo)
	for i := leftStart; i < state.windowStart; i++ {
		if preheatStartupDeferredBlock(state, i, width) {
			preheated = true
			state.preheatBlocksProcessed++
		}
	}
	rightEnd := min(len(state.allBlocks), state.windowEnd+startupDeferredTranscriptPreheatHalo)
	for i := state.windowEnd; i < rightEnd; i++ {
		if preheatStartupDeferredBlock(state, i, width) {
			preheated = true
			state.preheatBlocksProcessed++
		}
	}
	return preheated
}

func preheatStartupDeferredBlock(state *startupDeferredTranscriptState, index, width int) bool {
	if state == nil || index < 0 || index >= len(state.allBlocks) || index >= len(state.blockMeta) {
		return false
	}
	block := state.allBlocks[index]
	if block == nil {
		return false
	}
	meta := &state.blockMeta[index]
	lineCount := 0
	if block.lineCountCache > 0 && block.lineCacheWidth == width {
		lineCount = block.lineCountCache
	} else {
		lineCount = block.LineCount(width)
	}
	if lineCount > 0 {
		if meta.LineCounts == nil {
			meta.LineCounts = make(map[int]int, 1)
		}
		meta.LineCounts[width] = lineCount
	}
	if meta.SearchableText == "" {
		meta.SearchableText = block.searchableTextLower()
	}
	if meta.Summary == "" {
		meta.Summary = block.Summary()
	}
	return lineCount > 0
}
