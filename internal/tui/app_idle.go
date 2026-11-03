package tui

import (
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

const (
	// cacheSweepIdleDelay is the minimum duration the terminal must stay in
	// background and globally idle before render caches are dropped.
	cacheSweepIdleDelay = 5 * time.Minute
	// freeOSMemoryIdleDelay is how long the terminal must stay in background and
	// globally idle before a heap reclaim attempt.
	freeOSMemoryIdleDelay = 30 * time.Second
	// freeOSMemoryMinInterval caps how often a reclaim attempt may run,
	// including attempts skipped by the reclaimable threshold.
	freeOSMemoryMinInterval = 5 * time.Minute
	// freeOSMemoryReclaimableThreshold is the minimum HeapIdle-HeapReleased
	// worth forcing a full GC for.
	freeOSMemoryReclaimableThreshold = 8 << 20
	backgroundHousekeepingDelay      = 1 * time.Second
)

// Memory reduction during background idle happens in two independent steps:
//   - A heap reclaim attempt 30 seconds after the terminal became background-idle
//     and globally idle: read MemStats and, above a reclaimable threshold, run
//     debug.FreeOSMemory asynchronously so the forced GC + scavenge never blocks
//     Update.
//   - A cache sweep 5 minutes after that point: drop render caches for off-screen
//     blocks and shrink the hot budget to force aggressive spill
//     (performIdleSweep, on the Update path).
//
// Both are gated by the same global idle predicate and share one timer; neither
// resets the idle window, so their own minimum intervals decide when each runs
// again.
//
// The steps stay separate because their costs fall on different sides:
// FreeOSMemory returns idle heap pages but leaves render caches intact, so a
// return to the foreground costs nothing. Dropping caches forces cold re-renders
// of whatever the user scrolls back to, which is why it waits for a longer
// absence instead of riding along with the 30-second reclaim.

// idleSweepTickMsg is sent when the next idle maintenance deadline arrives.
type idleSweepTickMsg struct {
	generation uint64
}

// idleSweepTick returns a command that sends an idleSweepTickMsg after the
// provided delay.
func idleSweepTick(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = time.Millisecond
	}
	return tickCmd(delay, func(time.Time) tea.Msg {
		return idleSweepTickMsg{generation: generation}
	})
}

// scheduleIdleSweepTick schedules the next idle maintenance tick if not already
// scheduled.
func (m *Model) scheduleIdleSweepTick(delay time.Duration) tea.Cmd {
	if m.idleSweepScheduled {
		return nil
	}
	if delay <= 0 {
		delay = time.Millisecond
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

// hasActiveWorkForIdleReclaim reports whether any agent, job, draft, dialog, or
// session transition is still active. It is the single "globally idle" gate for
// background maintenance; states that cannot be classified count as busy so a
// forced GC never runs under live work.
func (m *Model) hasActiveWorkForIdleReclaim() bool {
	if m.hasOtherComposerDrafts(true) || len(m.queuedDrafts) > 0 || len(m.mailboxQueue) > 0 || m.inflightDraft != nil {
		return true
	}
	if len(m.activeJobs()) > 0 {
		return true
	}
	if m.agent != nil && (m.agent.LoopKeepsMainBusy() || m.agent.IsCompactionRunning()) {
		return true
	}
	if m.compactionBgStatus.Active || m.sessionSwitch.active() || m.takeoverInputFrozen() || m.dialogActive() {
		return true
	}
	// Activity events cover every running agent, so a streaming SubAgent is
	// caught here before the more expensive checks below.
	for _, activity := range m.activities {
		if runtimeActivityBusy(activity) {
			return true
		}
	}
	if m.viewport != nil && (m.viewport.HasUserLocalShellPending() || m.viewport.HasPendingToolWork()) {
		return true
	}
	if m.agent != nil {
		for _, sub := range m.agent.GetSubAgents() {
			if sub.State == string(agent.SubAgentStateRunning) {
				return true
			}
		}
	}
	return false
}

// updateBackgroundIdleSweepState starts or clears the background idle window
// and keeps one maintenance tick scheduled for the next deadline.
func (m *Model) updateBackgroundIdleSweepState() tea.Cmd {
	if m.displayState != stateBackground || m.hasActiveWorkForIdleReclaim() {
		m.cancelFreeOSMemoryReclaim()
		m.backgroundIdleSince = time.Time{}
		m.clearIdleSweepSchedule()
		return nil
	}
	if m.backgroundIdleSince.IsZero() {
		m.backgroundIdleSince = time.Now()
	}
	return m.scheduleIdleSweepTick(m.nextIdleMaintenanceDelay(time.Now()))
}

// nextIdleMaintenanceDelay returns the time until the earlier of the next heap
// reclaim attempt and the next cache sweep.
func (m *Model) nextIdleMaintenanceDelay(now time.Time) time.Duration {
	if m.backgroundIdleSince.IsZero() {
		return freeOSMemoryIdleDelay
	}
	cacheDue := m.backgroundIdleSince.Add(cacheSweepIdleDelay)
	if !m.lastCacheSweepAt.IsZero() {
		if t := m.lastCacheSweepAt.Add(cacheSweepIdleDelay); t.After(cacheDue) {
			cacheDue = t
		}
	}
	due := cacheDue
	if !m.freeOSMemoryPending {
		freeDue := m.backgroundIdleSince.Add(freeOSMemoryIdleDelay)
		if !m.lastFreeOSMemoryAt.IsZero() {
			if t := m.lastFreeOSMemoryAt.Add(freeOSMemoryMinInterval); t.After(freeDue) {
				freeDue = t
			}
		}
		if freeDue.Before(due) {
			due = freeDue
		}
	}
	if remaining := due.Sub(now); remaining > 0 {
		return remaining
	}
	return time.Millisecond
}

func shouldAttemptFreeOSMemory(now, idleSince, lastAttempt time.Time, pending bool) bool {
	if pending || idleSince.IsZero() {
		return false
	}
	if now.Sub(idleSince) < freeOSMemoryIdleDelay {
		return false
	}
	return lastAttempt.IsZero() || now.Sub(lastAttempt) >= freeOSMemoryMinInterval
}

// handleIdleSweepTick runs whichever maintenance action is due and schedules
// the next tick. The tick only arrives after the background idle window has
// held; every gate is re-checked because work may have resumed meantime.
func (m *Model) handleIdleSweepTick(msg idleSweepTickMsg) tea.Cmd {
	if msg.generation != m.idleSweepGeneration {
		return nil
	}
	m.idleSweepScheduled = false

	if m.displayState != stateBackground {
		m.cancelFreeOSMemoryReclaim()
		m.backgroundIdleSince = time.Time{}
		return nil
	}
	// The cached job snapshot may be frames old while idle; refresh it once on
	// this slow path so a finished job cannot hold the idle window closed.
	m.refreshJobSnapshot()
	if m.hasActiveWorkForIdleReclaim() {
		m.cancelFreeOSMemoryReclaim()
		m.backgroundIdleSince = time.Time{}
		return nil
	}
	if m.backgroundIdleSince.IsZero() {
		m.backgroundIdleSince = time.Now()
	}

	now := time.Now()
	var cmds []tea.Cmd
	if shouldAttemptFreeOSMemory(now, m.backgroundIdleSince, m.lastFreeOSMemoryAt, m.freeOSMemoryPending) {
		m.freeOSMemoryPending = true
		m.freeOSMemoryGeneration++
		gate := &freeOSMemoryGate{}
		m.freeOSMemoryGate = gate
		cmds = append(cmds, freeOSMemoryCmd(m.freeOSMemoryGeneration, gate))
	}
	if now.Sub(m.backgroundIdleSince) >= cacheSweepIdleDelay &&
		(m.lastCacheSweepAt.IsZero() || now.Sub(m.lastCacheSweepAt) >= cacheSweepIdleDelay) {
		m.performIdleSweep()
		m.lastCacheSweepAt = now
	}
	cmds = append(cmds, m.scheduleIdleSweepTick(m.nextIdleMaintenanceDelay(time.Now())))
	return tea.Batch(cmds...)
}

// freeOSMemoryResultMsg reports one heap reclaim attempt.
type freeOSMemoryResultMsg struct {
	generation  uint64
	reclaimable uint64
	ran         bool
}

// heapReclaimableBytes returns the idle heap spans the Go runtime could return
// to the OS. HeapReleased already went back; the difference is what a scavenge
// can still reclaim.
func heapReclaimableBytes(mem *runtime.MemStats) uint64 {
	if mem.HeapIdle <= mem.HeapReleased {
		return 0
	}
	return mem.HeapIdle - mem.HeapReleased
}

// freeOSMemoryGate couples a dispatched reclaim with the activity transition
// that invalidates it. Activity handlers cancel the gate before returning, so
// a reclaim that has not passed the gate will not start. A FreeOSMemory call
// that already passed the check cannot be preempted; cancellation does not wait
// for it, so resumed activity is never blocked by the full GC.
type freeOSMemoryGate struct {
	mu        sync.Mutex
	cancelled bool
}

func (g *freeOSMemoryGate) cancel() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.cancelled = true
	g.mu.Unlock()
}

func (g *freeOSMemoryGate) free(freeOSMemory func()) bool {
	if g == nil {
		freeOSMemory()
		return true
	}
	g.mu.Lock()
	cancelled := g.cancelled
	g.mu.Unlock()
	if cancelled {
		return false
	}
	freeOSMemory()
	return true
}

// cancelFreeOSMemoryReclaim invalidates an in-flight heap reclaim. It does not
// wait for a FreeOSMemory call that has already started: the full GC cannot be
// preempted, but resumed activity must not block on it.
func (m *Model) cancelFreeOSMemoryReclaim() {
	if m.freeOSMemoryGate == nil && !m.freeOSMemoryPending {
		return
	}
	if m.freeOSMemoryGate != nil {
		m.freeOSMemoryGate.cancel()
		m.freeOSMemoryGate = nil
	}
	m.freeOSMemoryPending = false
	m.freeOSMemoryGeneration++
}

// freeOSMemoryCmd measures the heap and forces a full GC + scavenge when the
// reclaimable part is worth it. It runs in a command goroutine because both
// runtime.ReadMemStats and FreeOSMemory stop the world.
func freeOSMemoryCmd(generation uint64, gate *freeOSMemoryGate) tea.Cmd {
	return runFreeOSMemoryCmd(generation, gate, runtime.ReadMemStats, debug.FreeOSMemory)
}

func runFreeOSMemoryCmd(generation uint64, gate *freeOSMemoryGate, readMemStats func(*runtime.MemStats), freeOSMemory func()) tea.Cmd {
	return func() tea.Msg {
		var mem runtime.MemStats
		readMemStats(&mem)
		reclaimable := heapReclaimableBytes(&mem)
		if reclaimable < freeOSMemoryReclaimableThreshold {
			return freeOSMemoryResultMsg{generation: generation, reclaimable: reclaimable}
		}
		if !gate.free(freeOSMemory) {
			return freeOSMemoryResultMsg{generation: generation, reclaimable: reclaimable}
		}
		return freeOSMemoryResultMsg{generation: generation, reclaimable: reclaimable, ran: true}
	}
}

func (m *Model) handleFreeOSMemoryResult(msg freeOSMemoryResultMsg) tea.Cmd {
	if msg.generation != m.freeOSMemoryGeneration {
		return nil
	}
	m.freeOSMemoryPending = false
	m.freeOSMemoryGate = nil
	m.lastFreeOSMemoryAt = time.Now()
	m.freeOSMemoryLastReclaimable = msg.reclaimable
	m.freeOSMemoryLastRan = msg.ran
	// Reschedule against the attempt timestamp; the timer set while this command
	// was pending predates it.
	m.clearIdleSweepSchedule()
	return m.updateBackgroundIdleSweepState()
}

// performIdleSweep executes the actual idle cleanup work.
// This function must be safe to call from the Bubble Tea Update path
// (i.e., no blocking I/O or heavy computation).
func (m *Model) performIdleSweep() {
	if m.viewport == nil {
		return
	}
	// Drop render caches for off-screen blocks, including the derived Markdown
	// and highlighter state that only a foreground sweep keeps.
	m.viewport.DropOffScreenCaches()
	// The deferred startup archive lives outside the viewport, so its previously
	// visited windows need their own sweep.
	m.dropStartupDeferredTranscriptHiddenCaches()

	// Shrink hot budget for aggressive spill.
	cadence := m.currentCadence()
	if cadence.aggressiveHotBudget {
		m.viewport.ShrinkHotBudget()
	}
}
