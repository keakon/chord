package tui

import (
	"fmt"
	"image"
	"strings"

	"github.com/keakon/chord/internal/analytics"
)

func (m *Model) renderUsageStatsDialog() string {
	innerWidth := m.usageStatsInnerWidth()
	lines := m.usageStatsLines(innerWidth)
	visible := min(m.usageStatsVisibleLines(), len(lines))
	start := max(min(max(m.usageStats.scrollOffset, 0), len(lines)-visible), 0)
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
	cfg := m.usageStatsOverlayConfig()
	if maxScroll := m.usageStatsMaxScroll(); maxScroll > 0 {
		cfg.Hint = appendHintText(cfg.Hint, fmt.Sprintf("%d/%d", start+visible, len(lines)))
	}
	dialog, _ := RenderOverlay(cfg, content, image.Rect(0, 0, m.width, m.height))

	m.usageStats.dialogCacheW = m.width
	m.usageStats.dialogCacheH = m.height
	m.usageStats.dialogCacheScroll = start
	m.usageStats.dialogCacheVer = m.usageStats.renderVersion
	m.usageStats.dialogCacheTheme = m.theme.Name
	m.usageStats.dialogCacheText = dialog
	return dialog
}

func (m *Model) usageStatsHint() string {
	if m.width < 60 {
		return hintLine(hint("Tab", "view"), hint("s", "scope"), hint("j/k", "scroll"), hint("Esc", "close"))
	}
	chips := []hintChip{hint("Tab/Shift+Tab", "view"), hint("s", "scope")}
	if m.usageStats.scope == statsScopeProject {
		if m.usageStats.projectLoadErr != "" && m.usageStats.projectReport == nil {
			chips = append(chips, hint("r", "retry"))
		} else {
			chips = append(chips, hint("r", "range"))
		}
	}
	chips = append(chips,
		hint("j/k", "scroll"), hint("g/G", "jump"), hint("Ctrl+f/b", "page"), hint("Esc/$", "close"),
	)
	return hintLine(chips...)
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
		return TabActiveStyle.Render(label)
	}
	return TabStyle.Render(label)
}

func renderUsageTable(columns []TableColumn, items []OverlayTableItem, width int) []string {
	if len(items) == 0 {
		return []string{DimStyle.Render("(no data)")}
	}
	table := NewOverlayTable(columns, items, len(items))
	table.SetShowSelection(false)
	return strings.Split(table.Render(width), "\n")
}
