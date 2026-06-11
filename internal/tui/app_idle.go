package tui

import (
	"runtime/debug"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const (
	// idleSweepDelay is the minimum duration the terminal must stay in background
	// and remain idle before an idle sweep runs.
	idleSweepDelay              = 5 * time.Minute
	backgroundHousekeepingDelay = 1 * time.Second
)

// Memory reduction during background idle happens in two steps:
//   - Dropping render caches for off-screen blocks and shrinking the hot
//     budget to force aggressive spill (performIdleSweep, on the Update path)
//   - A single debug.FreeOSMemory pass after each sweep, run asynchronously
//     via freeOSMemoryCmd so the forced GC + scavenge never blocks Update.
//
// FreeOSMemory is gated to background-idle sweeps only, which already enforce
// a minimum interval of idleSweepDelay between runs.

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

	return tea.Batch(freeOSMemoryCmd(), m.scheduleIdleSweepTick(idleSweepDelay))
}

// freeOSMemoryCmd returns the cached render memory released by the sweep back
// to the OS. It runs in a command goroutine because FreeOSMemory forces a full
// GC and scavenge, which can take tens of milliseconds on a large heap.
func freeOSMemoryCmd() tea.Cmd {
	return func() tea.Msg {
		debug.FreeOSMemory()
		return nil
	}
}

// performIdleSweep executes the actual idle cleanup work.
// This function must be safe to call from the Bubble Tea Update path
// (i.e., no blocking I/O or heavy computation).
func (m *Model) performIdleSweep() {
	if m.viewport == nil {
		return
	}
	// Drop render caches for off-screen blocks.
	m.viewport.DropOffScreenCaches()

	// Shrink hot budget for aggressive spill.
	cadence := m.currentCadence()
	if cadence.aggressiveHotBudget {
		m.viewport.ShrinkHotBudget()
	}
}
