package tui

import tea "charm.land/bubbletea/v2"

type uiEffects struct {
	followup        tea.Cmd
	refreshSidebar  bool
	invalidateUsage bool
}

func (e *uiEffects) addFollowup(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	e.followup = tea.Batch(e.followup, cmd)
}

func (e *uiEffects) merge(other uiEffects) {
	e.addFollowup(other.followup)
	e.refreshSidebar = e.refreshSidebar || other.refreshSidebar
	e.invalidateUsage = e.invalidateUsage || other.invalidateUsage
}

func (m *Model) applyUIEffects(e uiEffects) tea.Cmd {
	if m == nil {
		return e.followup
	}
	if e.refreshSidebar {
		e.invalidateUsage = true
		m.refreshSidebar()
	}
	if e.invalidateUsage {
		m.invalidateStatusBarAgentSnapshot()
		m.invalidateUsageStatsCache()
		m.cachedInfoPanelFP = ""
		m.cachedInfoPanelOut = ""
	}
	return e.followup
}
