package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	// startupDeferredArchiveSpillDelay spaces spill batches so a window switch
	// never waits on archive I/O: the batch runs on a later Update, after the
	// frame that made the blocks hidden.
	startupDeferredArchiveSpillDelay = 200 * time.Millisecond
	// Each tick spills a bounded amount and re-arms while work remains, which
	// keeps a large archive shrinking without one long blocking write.
	startupDeferredArchiveSpillBatchBlocks = 16
	startupDeferredArchiveSpillBatchBytes  = 1 << 20
	// A single archive block can exceed the per-batch target, especially when
	// the idle oversized allowance is active. Keep one synchronous spill
	// bounded by the same ceiling the idle oversized sweep uses; blocks above
	// it stay hot and hydrate on demand instead of blocking Update.
	startupDeferredArchiveSpillMaxBlockBytes = idleOversizedSpillBatchBytes
)

type startupDeferredArchiveSpillTickMsg struct {
	generation uint64
}

func startupDeferredArchiveSpillTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = startupDeferredArchiveSpillDelay
	}
	return tickCmd(delay, func(time.Time) tea.Msg {
		return startupDeferredArchiveSpillTickMsg{generation: generation}
	})
}

// scheduleStartupDeferredMaintenance turns pending deferred-transcript work
// into ticks. Handlers set the pending flags because most of them cannot return
// a command; Update calls this once per message so no path forgets to re-arm.
func (m *Model) scheduleStartupDeferredMaintenance() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 3)
	if cmd := m.maybeScheduleStartupDeferredPreheatFromPending(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.maybeScheduleStartupDeferredArchiveSpill(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if cmd := m.maybeScheduleIdleOversizedSpill(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	switch len(cmds) {
	case 0:
		return nil
	case 1:
		return cmds[0]
	default:
		return tea.Batch(cmds...)
	}
}

// markStartupDeferredArchiveSpillPending asks for a spill pass after the next
// Update. The archive holds every deferred block, so its hidden part is the
// only transcript memory that grows with session length and is not bounded by
// the viewport budget.
func (m *Model) markStartupDeferredArchiveSpillPending() {
	if m.startupDeferredTranscript == nil {
		return
	}
	m.startupDeferredArchiveSpillPending = true
}

func (m *Model) maybeScheduleStartupDeferredArchiveSpill() tea.Cmd {
	if m.startupDeferredTranscript == nil {
		m.startupDeferredArchiveSpillPending = false
		m.startupDeferredArchiveSpillScheduled = false
		return nil
	}
	if !m.startupDeferredArchiveSpillPending || m.startupDeferredArchiveSpillScheduled {
		return nil
	}
	m.startupDeferredArchiveSpillScheduled = true
	m.startupDeferredArchiveSpillGeneration++
	return startupDeferredArchiveSpillTick(m.startupDeferredArchiveSpillGeneration, startupDeferredArchiveSpillDelay)
}

func (m *Model) handleStartupDeferredArchiveSpillTick(msg startupDeferredArchiveSpillTickMsg) tea.Cmd {
	if msg.generation != m.startupDeferredArchiveSpillGeneration {
		return nil
	}
	m.startupDeferredArchiveSpillScheduled = false
	state := m.startupDeferredTranscript
	if state == nil || m.viewport == nil || m.viewport.spill == nil {
		m.startupDeferredArchiveSpillPending = false
		return nil
	}
	spilled, more := m.spillStartupDeferredArchiveBatch(state)
	if more && spilled > 0 {
		return m.maybeScheduleStartupDeferredArchiveSpill()
	}
	m.startupDeferredArchiveSpillPending = false
	return nil
}

// spillStartupDeferredArchiveBatch spills hidden archive blocks outside the
// active window and its preheat halo. The window stays hot because it is the
// visible transcript; the halo stays hot because scrolling or a window step
// lands there next. Everything else hydrates on demand through
// Block.ensureMaterialized, with the archive metadata keeping summaries and
// per-width line counts searchable without loading content.
func (m *Model) spillStartupDeferredArchiveBatch(state *startupDeferredTranscriptState) (spilled int, more bool) {
	if state == nil || m.viewport == nil || m.viewport.spill == nil {
		return 0, false
	}
	keepStart := max(state.windowStart-startupDeferredTranscriptPreheatHalo, 0)
	keepEnd := min(state.windowEnd+startupDeferredTranscriptPreheatHalo, len(state.allBlocks))
	var visible map[int]struct{}
	if len(m.viewport.blocks) > 0 {
		visible = make(map[int]struct{}, len(m.viewport.blocks))
		for _, block := range m.viewport.blocks {
			if block != nil {
				visible[block.ID] = struct{}{}
			}
		}
	}
	start := state.archiveSpillCursor
	if start < 0 || start >= len(state.allBlocks) {
		start = 0
	}
	allowOversized := false
	if state.archiveSpillAllowOversized && m.displayState == stateBackground {
		allowOversized = !m.hasActiveWorkForIdleReclaim()
	}
	var bytes int64
	for i := start; i < len(state.allBlocks); i++ {
		block := state.allBlocks[i]
		if i >= keepStart && i < keepEnd {
			continue
		}
		if !startupDeferredArchiveBlockSpillable(block, visible, m.viewport, allowOversized) {
			continue
		}
		size := block.estimatedHotBytes()
		if size > startupDeferredArchiveSpillMaxBlockBytes {
			continue
		}
		if spilled >= startupDeferredArchiveSpillBatchBlocks || bytes >= startupDeferredArchiveSpillBatchBytes {
			state.archiveSpillCursor = i
			return spilled, true
		}
		if !m.spillStartupDeferredArchiveBlock(state, i) {
			continue
		}
		spilled++
		bytes += size
	}
	state.archiveSpillCursor = 0
	return spilled, false
}

func startupDeferredArchiveBlockSpillable(block *Block, visible map[int]struct{}, viewport *Viewport, allowOversized bool) bool {
	if block == nil || block.spillCold {
		return false
	}
	if _, ok := visible[block.ID]; ok {
		return false
	}
	if block.Streaming || block.UserLocalShellPending || block.Focused {
		return false
	}
	if block.Type == BlockToolCall && !block.ResultDone {
		return false
	}
	// Large blocks stay resident on the active path so switching back to the
	// archive does not pay a full reload-and-render hitch. The idle oversized
	// sweep is the only path that deliberately crosses this boundary.
	if !allowOversized && viewport != nil && viewport.blockTooLargeToSpill(block) {
		return false
	}
	return true
}

func (m *Model) spillStartupDeferredArchiveBlock(state *startupDeferredTranscriptState, index int) bool {
	if state == nil || index < 0 || index >= len(state.allBlocks) || m.viewport == nil || m.viewport.spill == nil {
		return false
	}
	block := state.allBlocks[index]
	if block == nil || block.spillCold {
		return false
	}
	if block.spillLineCounts == nil {
		block.spillLineCounts = make(map[int]int, 1)
	}
	// The metadata is the archive's line-count source: it already holds counts
	// for previously rendered widths, and seeding the block with them lets a
	// later window switch measure offsets without a hydration and re-render.
	if index < len(state.blockMeta) {
		for width, count := range state.blockMeta[index].LineCounts {
			if count <= 0 {
				continue
			}
			if _, ok := block.spillLineCounts[width]; !ok {
				block.spillLineCounts[width] = count
			}
		}
	}
	width := m.viewport.width
	if width <= 0 {
		width = 80
	}
	_, ok := spillBlockPayload(block, m.viewport.spill, width, false)
	return ok
}
