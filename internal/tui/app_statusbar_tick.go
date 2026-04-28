package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

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

func (m *Model) statusBarNextRefreshDelayAt(now time.Time) time.Duration {
	if m == nil {
		return 0
	}
	if m.viewport != nil && m.viewport.HasUserLocalShellPending() {
		return 0
	}
	// When background compaction is active, tick every second so the elapsed
	// timer and breathing icon stay live even if the foreground agent is idle.
	if m.compactionBgStatus.Active {
		return nextTimeBucketTransition(now, time.Second)
	}
	statusActivity := m.activities[m.focusedAgentIDOrMain()]
	if statusActivity.Type == agent.ActivityCompacting {
		return nextTimeBucketTransition(now, time.Second)
	}
	if m.isFocusedAgentBusy() {
		return 0
	}
	if _, ok := m.latestStatusStartWall(m.focusedAgentIDOrMain()); ok {
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
