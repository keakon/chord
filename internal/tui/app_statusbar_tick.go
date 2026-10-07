package tui

import (
	"time"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
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
	return tickCmd(delay, func(time.Time) tea.Msg {
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
	var delay time.Duration
	if m.isFocusedAgentBusy() || (m.viewport != nil && m.viewport.HasUserLocalShellPending()) {
		unit := time.Second
		activity := m.activityForAgent(m.focusedAgentIDOrMain())
		if m.currentCadence().visualAnimDelay > 0 && (activity.Type == agent.ActivityConnecting || ((activity.Type == "" || activity.Type == agent.ActivityIdle) && m.inflightDraft != nil)) {
			unit = statusBarConnectingCadence
		}
		delay = nextTimeBucketTransition(now, unit)
	}
	if m.compactionBgStatus.Active {
		candidate := nextTimeBucketTransition(now, compactionPillBreathPhase)
		if delay == 0 || candidate < delay {
			delay = candidate
		}
	} else if compactionBackgroundStatusVisibleAt(m.compactionBgStatus, now) {
		candidate := min(nextTimeBucketTransition(now, time.Second), m.compactionBgStatus.TerminalAt.Add(compactionStatusTerminalDuration).Sub(now))
		if delay == 0 || candidate < delay {
			delay = candidate
		}
	}
	if delay > 0 {
		return delay
	}
	if m.focusedAgentCanShowIdleSince() {
		return nextTimeBucketTransition(now, time.Minute)
	}
	return 0
}

func (m *Model) scheduleStatusBarTick() tea.Cmd {
	return m.scheduleStatusBarTickAt(time.Now())
}

func (m *Model) scheduleStatusBarTickAt(now time.Time) tea.Cmd {
	if m == nil || m.statusBarTickScheduled {
		return nil
	}
	delay := m.statusBarNextRefreshDelayAt(now)
	if delay <= 0 {
		return nil
	}
	m.statusBarTickScheduled = true
	m.statusBarTickAt = now.Add(delay)
	return statusBarTickCmd(m.statusBarTickGeneration, delay)
}

func (m *Model) restartStatusBarTick() tea.Cmd {
	return m.restartStatusBarTickAt(time.Now())
}

func (m *Model) restartStatusBarTickAt(now time.Time) tea.Cmd {
	if m == nil {
		return nil
	}
	// Progress events share the pending tick unless a transition needs an
	// earlier refresh. Repeated updates must not create extra timers.
	delay := m.statusBarNextRefreshDelayAt(now)
	if m.statusBarTickScheduled && delay > 0 && !now.Add(delay).Before(m.statusBarTickAt) {
		return nil
	}
	m.statusBarTickGeneration++
	m.statusBarTickScheduled = false
	return m.scheduleStatusBarTickAt(now)
}
