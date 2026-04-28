package tui

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/keakon/chord/internal/analytics"
)

func (m *Model) renderUsageStatsDialog() string {
	innerWidth := m.usageStatsInnerWidth()
	lines := m.usageStatsLines(innerWidth)
	visible := m.usageStatsVisibleLines()
	if visible > len(lines) {
		visible = len(lines)
	}
	start := m.usageStats.scrollOffset
	if start < 0 {
		start = 0
	}
	if start > len(lines)-visible {
		start = len(lines) - visible
	}
	if start < 0 {
		start = 0
	}
	if m.usageStats.dialogCacheText != "" &&
		m.usageStats.dialogCacheW == m.width &&
		m.usageStats.dialogCacheH == m.height &&
		m.usageStats.dialogCacheScroll == start &&
		m.usageStats.dialogCacheVer == m.usageStats.renderVersion &&
		m.usageStats.dialogCacheTheme == m.theme.Name {
		return m.usageStats.dialogCacheText
	}
	contentLines := lines[start : start+visible]
	content := strings.Join(contentLines, "\n")
	scroll := ""
	if maxScroll := m.usageStatsMaxScroll(); maxScroll > 0 {
		scroll = fmt.Sprintf("  %d/%d", start+visible, len(lines))
	}
	dialog, _ := RenderOverlay(OverlayConfig{
		Title:    "Stats Panel",
		Hint:     m.usageStatsHint() + scroll,
		MinWidth: 60,
		MaxWidth: m.usageStatsMaxWidth(),
	}, content, lipgloss.Height(content), image.Rect(0, 0, m.width, m.height))
	m.usageStats.dialogCacheW = m.width
	m.usageStats.dialogCacheH = m.height
	m.usageStats.dialogCacheScroll = start
	m.usageStats.dialogCacheVer = m.usageStats.renderVersion
	m.usageStats.dialogCacheTheme = m.theme.Name
	m.usageStats.dialogCacheText = dialog
	return dialog
}

func (m *Model) usageStatsHint() string {
	base := "tab view  s scope  j/k scroll  g/G jump  ctrl+f/b page  $ close"
	if m.usageStats.scope == statsScopeProject {
		if m.usageStats.projectLoadErr != "" && m.usageStats.projectReport == nil {
			return "tab view  s scope  r retry  j/k scroll  g/G jump  ctrl+f/b page  $ close"
		}
		return "tab view  s scope  r range  j/k scroll  g/G jump  ctrl+f/b page  $ close"
	}
	return base
}

func (m *Model) renderUsageStatsScopeTabs() string {
	parts := []string{StatsTabLabelStyle.Render("Scope")}
	for _, scope := range []statsScope{statsScopeSession, statsScopeProject} {
		parts = append(parts, m.renderUsageStatsTab(scope.label(), scope == m.usageStats.scope))
	}
	return strings.Join(parts, " ")
}

func (m *Model) renderUsageStatsViewTabs() string {
	parts := []string{StatsTabLabelStyle.Render("View")}
	for _, view := range m.currentUsageStatsViews() {
		parts = append(parts, m.renderUsageStatsTab(view.label(), view == m.usageStats.view))
	}
	return strings.Join(parts, " ")
}

func (m *Model) renderUsageStatsRangeTabs() string {
	parts := []string{StatsTabLabelStyle.Render("Range")}
	for _, r := range []analytics.StatsRange{
		analytics.StatsRangeAllTime,
		analytics.StatsRangeLast30D,
		analytics.StatsRangeLast7D,
	} {
		parts = append(parts, m.renderUsageStatsTab(r.Label(), r == m.usageStats.rangeFilter))
	}
	return strings.Join(parts, " ")
}

func (m *Model) renderUsageStatsTab(label string, active bool) string {
	if active {
		return StatsTabActiveStyle.Render(label)
	}
	return StatsTabStyle.Render(label)
}

func renderUsageTable(columns []TableColumn, items []OverlayTableItem, width int) []string {
	if len(items) == 0 {
		return []string{DimStyle.Render("(no data)")}
	}
	table := NewOverlayTable(columns, items, len(items))
	table.SetShowSelection(false)
	return strings.Split(table.Render(width), "\n")
}
