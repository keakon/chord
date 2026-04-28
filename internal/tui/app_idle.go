package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	// idleSweepDelay is the minimum duration the terminal must stay in background
	// and remain idle before an idle sweep runs.
	idleSweepDelay              = 5 * time.Minute
	backgroundHousekeepingDelay = 1 * time.Second
)

// Note: We intentionally do NOT call runtime.GC() during idle sweep.
// The primary memory reduction mechanism is:
// 1. Dropping render caches for off-screen blocks (Phase 3)
// 2. Shrinking hot budget to force aggressive spill (Phase 4)
//
// Explicit GC is only worth considering if:
// - Cache drop + aggressive spill don't reduce RSS enough
// - A single low-frequency GC shows measurable benefit
// - It can be gated to background-idle only with minimum intervals
//
// Current implementation favors letting Go's GC run naturally after
// we've released references via cache drops.

// idleSweepTickMsg is sent when it's time to check for an idle sweep.
type idleSweepTickMsg struct {
	generation uint64
}

// idleSweepTick returns a command that sends an idleSweepTickMsg after the
// provided delay.
func idleSweepTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = idleSweepDelay
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return idleSweepTickMsg{generation: generation}
	})
}

// scheduleIdleSweepTick schedules an idle sweep tick if not already scheduled.
// The sweep timer is anchored to the moment the background view last became idle.
func (m *Model) scheduleIdleSweepTick(delay time.Duration) tea.Cmd {
	if m.idleSweepScheduled {
		return nil
	}
	if delay <= 0 {
		delay = idleSweepDelay
	}
	m.idleSweepScheduled = true
	m.idleSweepGeneration++
	return idleSweepTick(m.idleSweepGeneration, delay)
}

func (m *Model) clearIdleSweepSchedule() {
	if !m.idleSweepScheduled {
		return
	}
	m.idleSweepScheduled = false
	m.idleSweepGeneration++
}

func (m *Model) updateBackgroundIdleSweepState() tea.Cmd {
	if m.displayState != stateBackground {
		m.backgroundIdleSince = time.Time{}
		m.clearIdleSweepSchedule()
		return nil
	}
	if m.isAgentBusy() || m.confirm.request != nil || m.question.request != nil {
		m.backgroundIdleSince = time.Time{}
		m.clearIdleSweepSchedule()
		return nil
	}
	if m.backgroundIdleSince.IsZero() {
		m.backgroundIdleSince = time.Now()
	}
	remaining := idleSweepDelay - time.Since(m.backgroundIdleSince)
	if remaining <= 0 {
		remaining = time.Millisecond
	}
	return m.scheduleIdleSweepTick(remaining)
}

// handleIdleSweepTick checks if conditions are met for an idle sweep and
// executes it if so. An idle sweep:
//   - Drops render caches for off-screen blocks
//   - Requests aggressive spill of old blocks (shrinks hot budget)
//   - Does NOT block the main loop; all work is lightweight cache drops
func (m *Model) handleIdleSweepTick(msg idleSweepTickMsg) tea.Cmd {
	if msg.generation != m.idleSweepGeneration {
		return nil
	}
	m.idleSweepScheduled = false

	// Reject if conditions no longer hold.
	if m.displayState != stateBackground {
		m.backgroundIdleSince = time.Time{}
		return nil
	}
	if m.isAgentBusy() || m.confirm.request != nil || m.question.request != nil {
		m.backgroundIdleSince = time.Time{}
		return nil
	}
	if m.backgroundIdleSince.IsZero() {
		m.backgroundIdleSince = time.Now()
	}
	if time.Since(m.backgroundIdleSince) < idleSweepDelay {
		remaining := idleSweepDelay - time.Since(m.backgroundIdleSince)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
		return m.scheduleIdleSweepTick(remaining)
	}
	// Ensure enough time has passed since last sweep.
	if !m.lastSweepAt.IsZero() && time.Since(m.lastSweepAt) < idleSweepDelay {
		remaining := idleSweepDelay - time.Since(m.lastSweepAt)
		if remaining <= 0 {
			remaining = time.Millisecond
		}
		return m.scheduleIdleSweepTick(remaining)
	}

	// Run the idle sweep.
	m.performIdleSweep()
	m.lastSweepAt = time.Now()
	m.backgroundIdleSince = m.lastSweepAt

	return m.scheduleIdleSweepTick(idleSweepDelay)
}

// performIdleSweep executes the actual idle cleanup work.
// This function must be safe to call from the Bubble Tea Update path
// (i.e., no blocking I/O or heavy computation).
func (m *Model) performIdleSweep() {
	if m.viewport == nil {
		return
	}
	// Phase 3: drop render caches for off-screen blocks.
	m.viewport.DropOffScreenCaches()

	// Phase 4: shrink hot budget for aggressive spill.
	cadence := m.currentCadence()
	if cadence.aggressiveHotBudget {
		m.viewport.ShrinkHotBudget()
	}
}
