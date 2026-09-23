package tui

import (
	"time"

	"github.com/keakon/chord/internal/agent"
)

// statusBarDynamicCacheKeyAt composes the focused-agent state the status bar
// renderer feeds into the dynamic cache key, so tests can assert key transitions
// without rebuilding that state.
func (m *Model) statusBarDynamicCacheKeyAt(now time.Time) string {
	focused := m.focusedAgentIDOrMain()
	latestStatusStart := m.focusedAgentCanShowIdleSince()
	return m.statusBarDynamicCacheKeyFromState(
		now,
		m.viewport != nil && m.viewport.HasUserLocalShellPending(),
		m.renderRequestProgressSummary(focused),
		m.activityForAgent(focused).Type == agent.ActivityCompacting,
		m.isFocusedAgentBusy(),
		latestStatusStart,
	)
}
