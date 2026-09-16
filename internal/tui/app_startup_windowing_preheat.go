package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
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
	return tickCmd(delay, func(time.Time) tea.Msg {
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

// restartDeferredTranscriptPreheatAfterResize re-arms the halo preheat when a
// terminal resize changed the viewport width while a deferred transcript is
// active. Halo line counts are recorded per width and the preheat tick stops
// re-arming itself once the halo is covered, so a width change has to restart
// it explicitly.
func (m *Model) restartDeferredTranscriptPreheatAfterResize(previousViewportWidth int) tea.Cmd {
	if m == nil || m.viewport == nil || m.startupDeferredTranscript == nil {
		return nil
	}
	if m.viewport.width == previousViewportWidth {
		return nil
	}
	return m.restartStartupDeferredTranscriptPreheat(startupDeferredTranscriptPreheatDelay)
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
	if !preheated {
		// Every halo block already carries a line count for the current width,
		// so re-arming the timer would only re-scan the same neighbors and
		// re-run the budget enforcement every tick. A window switch or a width
		// change restarts the preheat when the halo needs measuring again.
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

// preheatStartupDeferredBlock measures one halo block at the viewport width and
// records its line count in the archive metadata. It reports whether the block
// needed a render: a block that already carries a count for this width is left
// warm but is not new work, which is what lets the preheat timer stop once the
// halo is covered.
func preheatStartupDeferredBlock(state *startupDeferredTranscriptState, index, width int) bool {
	if state == nil || index < 0 || index >= len(state.allBlocks) || index >= len(state.blockMeta) {
		return false
	}
	block := state.allBlocks[index]
	if block == nil {
		return false
	}
	meta := &state.blockMeta[index]
	warm := block.lineCountCache > 0 && block.lineCacheWidth == width
	lineCount := 0
	if warm {
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
	return !warm && lineCount > 0
}
