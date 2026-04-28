package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

func (m *Model) usageStatsLines(width int) []string {
	if width < 40 {
		width = 40
	}
	if m.usageStats.linesCacheLines != nil && m.usageStats.linesCacheWidth == width && m.usageStats.linesCacheVer == m.usageStats.renderVersion {
		return m.usageStats.linesCacheLines
	}

	lines := []string{
		m.renderUsageStatsScopeTabs(),
		m.renderUsageStatsViewTabs(),
	}
	if m.usageStats.scope == statsScopeProject {
		lines = append(lines, m.renderUsageStatsRangeTabs())
	}
	lines = append(lines, "")
	lines = append(lines, m.usageStatsContentLines(width)...)
	m.usageStats.linesCacheWidth = width
	m.usageStats.linesCacheVer = m.usageStats.renderVersion
	m.usageStats.linesCacheLines = lines
	return lines
}

func (m *Model) usageStatsContentLines(width int) []string {
	switch m.usageStats.scope {
	case statsScopeProject:
		return m.projectUsageStatsLines(width)
	default:
		return m.sessionUsageStatsLines(width)
	}
}

func (m *Model) sessionUsageStatsLines(width int) []string {
	titlePrefix := "Session "
	if m.agent == nil {
		return []string{
			DialogTitleStyle.Render(titlePrefix + m.usageStats.view.label()),
			DimStyle.Render("Usage statistics unavailable."),
		}
	}

	stats := m.agent.GetUsageStats()
	switch m.usageStats.view {
	case statsViewModels:
		lines := []string{DialogTitleStyle.Render(titlePrefix + "Models")}
		lines = append(lines, renderUsageTable(
			[]TableColumn{
				{Title: "Model", Width: 0, Align: 0},
				{Title: "Calls", Width: 6, Align: 1},
				{Title: "Input", Width: 8, Align: 1},
				{Title: "Output", Width: 8, Align: 1},
				{Title: "Cost", Width: 8, Align: 1},
			},
			usageModelItems(stats.ByModel),
			width,
		)...)
		return lines
	case statsViewAgents:
		lines := []string{DialogTitleStyle.Render(titlePrefix + "Agents")}
		lines = append(lines, renderUsageTable(
			[]TableColumn{
				{Title: "Agent", Width: 0, Align: 0},
				{Title: "Calls", Width: 6, Align: 1},
				{Title: "Input", Width: 8, Align: 1},
				{Title: "Output", Width: 8, Align: 1},
				{Title: "Cost", Width: 8, Align: 1},
			},
			usageAgentItems(stats.ByAgent),
			width,
		)...)
		return lines
	default:
		return []string{
			DialogTitleStyle.Render(titlePrefix + "Overview"),
			fmt.Sprintf("Calls: %d", stats.LLMCalls),
			fmt.Sprintf("Input: %s    Output: %s", formatUsageTokens(stats.InputTokens), formatUsageTokens(stats.OutputTokens)),
			fmt.Sprintf("Cache R: %s    Cache W: %s", formatUsageTokens(stats.CacheReadTokens), formatUsageTokens(stats.CacheWriteTokens)),
			fmt.Sprintf("Reasoning: %s    Cost: %s", formatUsageTokens(stats.ReasoningTokens), formatCost(stats.EstimatedCost)),
		}
	}
}

func (m *Model) projectUsageStatsLines(width int) []string {
	titlePrefix := "Project "
	switch {
	case m.usageStats.projectLoading:
		return []string{
			DialogTitleStyle.Render(titlePrefix + m.usageStats.view.label()),
			DimStyle.Render("Loading project statistics..."),
		}
	case m.usageStats.projectLoadErr != "":
		return []string{
			DialogTitleStyle.Render(titlePrefix + m.usageStats.view.label()),
			ErrorStyle.Render(fmt.Sprintf("Failed to load project statistics: %s", m.usageStats.projectLoadErr)),
			DimStyle.Render("Press r to retry."),
		}
	case m.usageStats.projectReport == nil:
		return []string{
			DialogTitleStyle.Render(titlePrefix + m.usageStats.view.label()),
			DimStyle.Render("Project statistics unavailable."),
		}
	}

	report := m.usageStats.projectReport
	switch m.usageStats.view {
	case statsViewModels:
		lines := []string{DialogTitleStyle.Render(titlePrefix + "Models")}
		lines = append(lines, renderUsageTable(
			[]TableColumn{
				{Title: "Model", Width: 0, Align: 0},
				{Title: "Calls", Width: 6, Align: 1},
				{Title: "Input", Width: 8, Align: 1},
				{Title: "Output", Width: 8, Align: 1},
				{Title: "Cost", Width: 8, Align: 1},
			},
			usageRefAggregateItemsByUsage(report.ByModelRef, func(key string) string { return key }),
			width,
		)...)
		return lines
	case statsViewAgents:
		lines := []string{DialogTitleStyle.Render(titlePrefix + "Agents")}
		lines = append(lines, renderUsageTable(
			[]TableColumn{
				{Title: "Agent", Width: 0, Align: 0},
				{Title: "Calls", Width: 6, Align: 1},
				{Title: "Input", Width: 8, Align: 1},
				{Title: "Output", Width: 8, Align: 1},
				{Title: "Cost", Width: 8, Align: 1},
			},
			usageRefAggregateItemsByUsage(report.ByAgent, func(key string) string {
				if key == "main" {
					return "main"
				}
				return key
			}),
			width,
		)...)
		return lines
	case statsViewDates:
		lines := []string{DialogTitleStyle.Render(titlePrefix + "Dates")}
		lines = append(lines, renderUsageTable(
			[]TableColumn{
				{Title: "Date", Width: 10, Align: 0},
				{Title: "Calls", Width: 6, Align: 1},
				{Title: "Input", Width: 8, Align: 1},
				{Title: "Output", Width: 8, Align: 1},
				{Title: "Cost", Width: 8, Align: 1},
			},
			usageDateAggregateItems(report.ByDate, func(key string) string { return key }),
			width,
		)...)
		return lines
	default:
		if report.SessionCount == 0 {
			return []string{
				DialogTitleStyle.Render(titlePrefix + "Overview"),
				DimStyle.Render(fmt.Sprintf("No project usage found for %s.", report.Range.Label())),
			}
		}
		return []string{
			DialogTitleStyle.Render(titlePrefix + "Overview"),
			fmt.Sprintf("Range: %s", report.Range.Label()),
			fmt.Sprintf("Sessions: %d    Active days: %d", report.SessionCount, report.ActiveDays),
			fmt.Sprintf("Calls: %d", report.UsageTotal.LLMCalls),
			fmt.Sprintf("Input: %s    Output: %s", formatUsageTokens(report.UsageTotal.InputTokens), formatUsageTokens(report.UsageTotal.OutputTokens)),
			fmt.Sprintf("Cache R: %s    Cache W: %s", formatUsageTokens(report.UsageTotal.CacheReadTokens), formatUsageTokens(report.UsageTotal.CacheWriteTokens)),
			fmt.Sprintf("Reasoning: %s    Cost: %s", formatUsageTokens(report.UsageTotal.ReasoningTokens), formatCost(report.UsageTotal.TotalCost)),
			fmt.Sprintf("First active: %s", formatUsageTime(report.FirstEventAt)),
			fmt.Sprintf("Last active: %s", formatUsageTime(report.LastEventAt)),
			DimStyle.Render(fmt.Sprintf("Source: %s", usageStatsProjectSourceHint(m.usageStatsProjectRoot()))),
		}
	}
}

func usageStatsProjectSourceHint(projectRoot string) string {
	locator, err := config.DefaultPathLocator()
	if err != nil {
		return "sessions"
	}
	if strings.TrimSpace(projectRoot) == "" {
		return locator.SessionsRoot
	}
	pl, err := locator.LocateProject(projectRoot)
	if err != nil {
		return locator.SessionsRoot
	}
	return pl.ProjectSessionsDir
}

func formatUsageTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04")
}

func formatUsageTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
