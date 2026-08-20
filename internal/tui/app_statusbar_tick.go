package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

const compactionStatusTerminalDuration = 2 * time.Second

// compactionPillBreathPhase is shared by the foreground and background
// compaction indicators. Keeping the refresh boundary and icon phase the same
// prevents the animation from changing speed when another activity starts.
// Keep the compaction indicator deliberately calm: compaction usually takes
// much longer than a normal request, so a rapid toggle makes a healthy slow
// operation look like a UI problem.
const compactionPillBreathPhase = time.Second

func compactionPillIconAt(now time.Time) string {
	if now.UnixMilli()/compactionPillBreathPhase.Milliseconds()%2 == 0 {
		return "■"
	}
	return "▪"
}

func statusBarTickCmd(generation uint64, delay time.Duration) tea.Cmd {
	if delay <= 0 {
		delay = time.Second
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return statusBarTickMsg{generation: generation}
	})
}

func nextTimeBucketTransition(now time.Time, unit time.Duration) time.Duration {
	if unit <= 0 {
		return 0
	}
	next := now.Truncate(unit).Add(unit)
	if !next.After(now) {
		next = now.Add(unit)
	}
	return next.Sub(now)
}

func compactionBackgroundStatusVisibleAt(status compactionBackgroundStatus, now time.Time) bool {
	if status.Active {
		return true
	}
	return status.Terminal != "" && !status.TerminalAt.IsZero() && now.Sub(status.TerminalAt) < compactionStatusTerminalDuration
}

func (m *Model) statusBarNextRefreshDelayAt(now time.Time) time.Duration {
	if m == nil {
		return 0
	}
	if m.viewport != nil && m.viewport.HasUserLocalShellPending() {
		return 0
	}
	// The active compaction indicator owns a fixed animation cadence. Without
	// this, it refreshed once per second while idle but happened to refresh on
	// the faster activity ticker during another model request.
	if m.compactionBgStatus.Active {
		return nextTimeBucketTransition(now, compactionPillBreathPhase)
	}
	if compactionBackgroundStatusVisibleAt(m.compactionBgStatus, now) {
		return min(nextTimeBucketTransition(now, time.Second), m.compactionBgStatus.TerminalAt.Add(compactionStatusTerminalDuration).Sub(now))
	}
	if m.isFocusedAgentBusy() {
		return 0
	}
	if m.focusedAgentCanShowIdleSince() {
		return nextTimeBucketTransition(now, time.Minute)
	}
	return 0
}

func (m *Model) scheduleStatusBarTick() tea.Cmd {
	if m == nil || m.statusBarTickScheduled {
		return nil
	}
	delay := m.statusBarNextRefreshDelayAt(time.Now())
	if delay <= 0 {
		return nil
	}
	m.statusBarTickScheduled = true
	return statusBarTickCmd(m.statusBarTickGeneration, delay)
}

func (m *Model) restartStatusBarTick() tea.Cmd {
	if m == nil {
		return nil
	}
	m.statusBarTickGeneration++
	m.statusBarTickScheduled = false
	return m.scheduleStatusBarTick()
}
