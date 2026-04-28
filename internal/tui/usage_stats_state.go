package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/keakon/chord/internal/analytics"
)

type statsScope int

const (
	statsScopeSession statsScope = iota
	statsScopeProject
)

func (s statsScope) label() string {
	switch s {
	case statsScopeProject:
		return "Project"
	default:
		return "Session"
	}
}

type statsView int

const (
	statsViewOverview statsView = iota
	statsViewModels
	statsViewAgents
	statsViewDates
)

func (v statsView) label() string {
	switch v {
	case statsViewModels:
		return "Models"
	case statsViewAgents:
		return "Agents"
	case statsViewDates:
		return "Dates"
	default:
		return "Overview"
	}
}

type usageStatsState struct {
	scrollOffset   int
	prevMode       Mode
	scope          statsScope
	view           statsView
	rangeFilter    analytics.StatsRange
	projectLoading bool
	projectLoadErr string
	projectReport  *analytics.ProjectUsageReport

	renderVersion     uint64
	linesCacheWidth   int
	linesCacheVer     uint64
	linesCacheLines   []string
	dialogCacheW      int
	dialogCacheH      int
	dialogCacheScroll int
	dialogCacheVer    uint64
	dialogCacheTheme  string
	dialogCacheText   string
}

type projectUsageLoadedMsg struct {
	rangeFilter analytics.StatsRange
	report      *analytics.ProjectUsageReport
	err         error
}

func loadProjectUsageReportCmd(projectRoot string, rangeFilter analytics.StatsRange) tea.Cmd {
	projectRoot = strings.TrimSpace(projectRoot)
	return func() tea.Msg {
		report, err := analytics.BuildProjectUsageReport(projectRoot, rangeFilter)
		return projectUsageLoadedMsg{rangeFilter: rangeFilter, report: report, err: err}
	}
}

func (m *Model) invalidateUsageStatsCache() {
	m.usageStats.renderVersion++
	m.usageStats.linesCacheWidth = 0
	m.usageStats.linesCacheVer = 0
	m.usageStats.linesCacheLines = nil
	m.usageStats.dialogCacheW = 0
	m.usageStats.dialogCacheH = 0
	m.usageStats.dialogCacheScroll = 0
	m.usageStats.dialogCacheVer = 0
	m.usageStats.dialogCacheTheme = ""
	m.usageStats.dialogCacheText = ""
	if m.mode == ModeUsageStats {
		m.clampUsageStatsScroll()
	}
}

func (m *Model) openUsageStats() {
	if m.mode == ModeUsageStats {
		return
	}
	prevMode := m.mode
	m.clearActiveSearch()
	if prevMode == ModeInsert {
		m.input.Blur()
	}
	m.clearChordState()
	m.usageStats = usageStatsState{
		prevMode:    prevMode,
		scope:       statsScopeSession,
		view:        statsViewOverview,
		rangeFilter: analytics.StatsRangeAllTime,
	}
	m.invalidateUsageStatsCache()
	m.mode = ModeUsageStats
	m.recalcViewportSize()
}

func (m *Model) closeUsageStats() tea.Cmd {
	if m.mode != ModeUsageStats {
		return nil
	}
	prevMode := m.usageStats.prevMode
	m.usageStats = usageStatsState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) handleUsageStatsKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q":
		return m.closeUsageStats()
	case "tab":
		m.cycleUsageStatsView(1)
	case "shift+tab":
		m.cycleUsageStatsView(-1)
	case "s":
		return m.toggleUsageStatsScope()
	case "r":
		return m.handleUsageStatsRangeKey()
	case "j", "down":
		m.usageStats.scrollOffset++
	case "k", "up":
		m.usageStats.scrollOffset--
	case "ctrl+f":
		m.usageStats.scrollOffset += m.usageStatsVisibleLines()
	case "ctrl+b":
		m.usageStats.scrollOffset -= m.usageStatsVisibleLines()
	case "g":
		m.usageStats.scrollOffset = 0
	case "G":
		m.usageStats.scrollOffset = m.usageStatsMaxScroll()
	default:
		if keyMatches(msg.String(), m.keyMap.UsageStats) {
			return m.closeUsageStats()
		}
	}
	m.clampUsageStatsScroll()
	return nil
}

func (m *Model) toggleUsageStatsScope() tea.Cmd {
	switch m.usageStats.scope {
	case statsScopeProject:
		m.usageStats.scope = statsScopeSession
	default:
		m.usageStats.scope = statsScopeProject
	}
	m.usageStats.scrollOffset = 0
	m.invalidateUsageStatsCache()
	if !m.currentUsageStatsScopeSupports(m.usageStats.view) {
		m.usageStats.view = statsViewOverview
	}
	if m.usageStats.scope == statsScopeProject {
		return m.ensureProjectUsageReport()
	}
	return nil
}

func (m *Model) handleUsageStatsRangeKey() tea.Cmd {
	if m.usageStats.scope != statsScopeProject {
		return nil
	}
	if m.usageStats.projectLoadErr != "" && m.usageStats.projectReport == nil {
		return m.reloadProjectUsageReport()
	}
	m.usageStats.rangeFilter = nextStatsRange(m.usageStats.rangeFilter)
	m.usageStats.scrollOffset = 0
	m.invalidateUsageStatsCache()
	return m.reloadProjectUsageReport()
}

func (m *Model) reloadProjectUsageReport() tea.Cmd {
	m.usageStats.projectReport = nil
	m.usageStats.projectLoadErr = ""
	m.usageStats.projectLoading = true
	m.invalidateUsageStatsCache()
	return loadProjectUsageReportCmd(m.usageStatsProjectRoot(), m.usageStats.rangeFilter)
}

func (m *Model) ensureProjectUsageReport() tea.Cmd {
	if m.usageStats.scope != statsScopeProject {
		return nil
	}
	if m.usageStats.projectLoading {
		return nil
	}
	if m.usageStats.projectReport != nil && m.usageStats.projectLoadErr == "" {
		return nil
	}
	return m.reloadProjectUsageReport()
}

func nextStatsRange(current analytics.StatsRange) analytics.StatsRange {
	switch current {
	case analytics.StatsRangeLast30D:
		return analytics.StatsRangeLast7D
	case analytics.StatsRangeLast7D:
		return analytics.StatsRangeAllTime
	default:
		return analytics.StatsRangeLast30D
	}
}

func (m *Model) cycleUsageStatsView(delta int) {
	views := m.currentUsageStatsViews()
	if len(views) == 0 {
		return
	}
	idx := 0
	for i, view := range views {
		if view == m.usageStats.view {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(views)) % len(views)
	m.usageStats.view = views[idx]
	m.usageStats.scrollOffset = 0
	m.invalidateUsageStatsCache()
}

func (m *Model) currentUsageStatsViews() []statsView {
	if m.usageStats.scope == statsScopeProject {
		return []statsView{statsViewOverview, statsViewModels, statsViewAgents, statsViewDates}
	}
	return []statsView{statsViewOverview, statsViewModels, statsViewAgents}
}

func (m *Model) currentUsageStatsScopeSupports(view statsView) bool {
	for _, candidate := range m.currentUsageStatsViews() {
		if candidate == view {
			return true
		}
	}
	return false
}

func (m *Model) usageStatsProjectRoot() string {
	root := strings.TrimSpace(m.workingDir)
	if m.agent != nil {
		if p, ok := m.agent.(interface{ ProjectRoot() string }); ok {
			if v := strings.TrimSpace(p.ProjectRoot()); v != "" {
				root = v
			}
		}
	}
	return root
}

func (m *Model) usageStatsVisibleLines() int {
	visible := m.height - 12
	if visible < 8 {
		visible = 8
	}
	return visible
}

func (m *Model) usageStatsMaxWidth() int {
	maxWidth := m.width - 12
	if maxWidth > 110 {
		maxWidth = 110
	}
	if maxWidth < 60 {
		maxWidth = 60
	}
	return maxWidth
}

func (m *Model) usageStatsInnerWidth() int {
	return m.usageStatsMaxWidth() - 4
}

func (m *Model) usageStatsMaxScroll() int {
	lines := m.usageStatsLines(m.usageStatsInnerWidth())
	maxScroll := len(lines) - m.usageStatsVisibleLines()
	if maxScroll < 0 {
		maxScroll = 0
	}
	return maxScroll
}

func (m *Model) clampUsageStatsScroll() {
	if m.usageStats.scrollOffset < 0 {
		m.usageStats.scrollOffset = 0
	}
	if maxScroll := m.usageStatsMaxScroll(); m.usageStats.scrollOffset > maxScroll {
		m.usageStats.scrollOffset = maxScroll
	}
}
