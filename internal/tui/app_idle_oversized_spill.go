package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	// idleOversizedSpillDelay leaves the foreground enough time to resume
	// before the first batch runs; the pending request is cancelled on focus.
	idleOversizedSpillDelay = 200 * time.Millisecond
	// The idle sweep keeps each batch small so a large set of oversized cards
	// never turns one Update into a multi-block disk write.
	idleOversizedSpillBatchBlocks = 2
	idleOversizedSpillBatchBytes  = 8 << 20
)

type idleOversizedSpillTickMsg struct {
	generation uint64
}

func idleOversizedSpillTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = idleOversizedSpillDelay
	}
	return tickCmd(delay, func(time.Time) tea.Msg {
		return idleOversizedSpillTickMsg{generation: generation}
	})
}

func (m *Model) markIdleOversizedSpillPending() {
	if m == nil || m.viewport == nil || m.viewport.spill == nil {
		return
	}
	m.idleOversizedSpillPending = true
}

func (m *Model) maybeScheduleIdleOversizedSpill() tea.Cmd {
	if m == nil || m.viewport == nil || m.viewport.spill == nil {
		m.cancelIdleOversizedSpill()
		return nil
	}
	if !m.idleOversizedSpillPending || m.idleOversizedSpillScheduled {
		return nil
	}
	m.idleOversizedSpillScheduled = true
	m.idleOversizedSpillGeneration++
	return idleOversizedSpillTick(m.idleOversizedSpillGeneration, idleOversizedSpillDelay)
}

func (m *Model) handleIdleOversizedSpillTick(msg idleOversizedSpillTickMsg) tea.Cmd {
	if m == nil || msg.generation != m.idleOversizedSpillGeneration {
		return nil
	}
	m.idleOversizedSpillScheduled = false
	if m.viewport == nil || m.viewport.spill == nil || m.displayState != stateBackground || m.hasActiveWorkForIdleReclaim() {
		m.cancelIdleOversizedSpill()
		return nil
	}
	spilled, more := m.viewport.SpillOffScreenOversizedBlocksBatch(idleOversizedSpillBatchBlocks, idleOversizedSpillBatchBytes)
	if more && spilled > 0 {
		return m.maybeScheduleIdleOversizedSpill()
	}
	m.idleOversizedSpillPending = false
	return nil
}

func (m *Model) cancelIdleOversizedSpill() {
	if m == nil {
		return
	}
	m.idleOversizedSpillPending = false
	m.idleOversizedSpillScheduled = false
	m.idleOversizedSpillGeneration++
}
