package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/tui/modelref"
)

const infoPanelCollapsibleContentInset = 2

func joinInfoPanelBlockLines(lines []string) string {
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return lines[0]
	default:
		var b strings.Builder
		for i, line := range lines {
			if i > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(line)
		}
		return b.String()
	}
}

func renderInfoPanelCollapsibleContentLine(lineW int, content string) string {
	return renderInfoPanelIndentedLine(lineW, infoPanelCollapsibleContentInset, content)
}

// infoPanelBreakpoint returns the effective breakpoint tier for the given panel width.
// Tier 0 (width < 24): Only show critical summary (MODEL, context percentage, core status).
// Tier 1 (24 <= width < 32): Show Context + Token summary on single line.
// Tier 2 (32 <= width < 40): Show Context, Token, Cost with compact cache details.
// Tier 3 (width >= 40): Full compact layout including cache R/W and all details.
func infoPanelBreakpoint(panelWidth int) int {
	switch {
	case panelWidth < 24:
		return 0
	case panelWidth < 32:
		return 1
	case panelWidth < 40:
		return 2
	default:
		return 3
	}
}

func renderInfoPanelIndentedLine(lineW, inset int, content string) string {
	if inset < 0 {
		inset = 0
	}
	prefix := strings.Repeat(" ", inset)
	line := lipgloss.JoinHorizontal(lipgloss.Left, InfoPanelDim.Render(prefix), content)
	return InfoPanelLineBg.Width(lineW).Render(line)
}

func (m *Model) buildInfoPanelModelBlock(lineW int) string {
	// RunningModelRef may temporarily omit provider or @variant; backfill from selected ref.
	modelDisplay := modelref.FormatRunningModelRefForDisplay(m.agent.RunningModelRef(), m.agent.ProviderModelRef(), m.agent.RunningVariant(), lineW)
	modelShown := modelDisplay
	modelLines := []string{
		InfoPanelLineBg.Width(lineW).Render(InfoPanelTitle.Render("MODEL")),
		InfoPanelLineBg.Width(lineW).Render(InfoPanelValue.Render(modelShown)),
	}
	keysConfirmed, keysTotal := m.agent.KeyStats()
	if keysTotal > 1 {
		keysStr := fmt.Sprintf("Keys: %d/%d", keysConfirmed, keysTotal)
		keysStyle := InfoPanelValue
		switch keyPoolHealthSeverity(keysConfirmed, keysTotal) {
		case keyPoolSeverityCritical:
			keysStyle = InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelKeyCriticalFg))
		case keyPoolSeverityWarn:
			keysStyle = InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelKeyWarnFg))
		}
		modelLines = append(modelLines, InfoPanelLineBg.Width(lineW).Render(keysStyle.Render(keysStr)))
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(modelLines))
}

type keyPoolSeverity uint8

const (
	keyPoolSeverityNormal keyPoolSeverity = iota
	keyPoolSeverityWarn
	keyPoolSeverityCritical
)

func keyPoolHealthSeverity(healthy, total int) keyPoolSeverity {
	if total <= 0 {
		return keyPoolSeverityNormal
	}
	ratio := float64(healthy) / float64(total)
	switch {
	case ratio > 0.5:
		return keyPoolSeverityNormal
	case ratio > 0.25:
		return keyPoolSeverityWarn
	default:
		return keyPoolSeverityCritical
	}
}

func (m *Model) buildInfoPanelUsageBlock(width, lineW int) string {
	// current = last round input+cache (input-side tokens that occupy the context window); percent = current/limit.
	// Color by usage: green normal, orange >50%, red >80% (value and gauge match).
	current, limit := m.agent.GetContextStats()
	percent := 0.0
	if limit > 0 {
		percent = float64(current) / float64(limit)
	}

	bp := infoPanelBreakpoint(width)
	stats := m.agent.GetSidebarUsageStats()
	usageLines := []string{InfoPanelLineBg.Width(lineW).Render(InfoPanelTitle.Render("USAGE"))}
	if current > 0 || limit > 0 {
		// Tier 0: show only context percent in the header summary line.
		// Tier >=1: show context value + gauge.
		if bp >= 1 {
			gauge := m.renderContextGauge(width-6, percent)
			contextValueStr := fmt.Sprintf("%s (%s)", formatTokens(current), formatPercent(percent))
			usageLines = append(usageLines,
				renderInfoPanelKVLine(lineW, "Context", contextValueStyle(percent).Render(contextValueStr)),
				InfoPanelLineBg.Width(lineW).Render(gauge),
			)
			if msgCount := m.agent.GetContextMessageCount(); msgCount >= 0 {
				usageLines = append(usageLines, renderInfoPanelKVLine(lineW, "Messages", InfoPanelValue.Render(formatTokens(msgCount))))
			}
		}
	}

	if usageSummary := renderUsageSummaryLine(lineW, stats); usageSummary != "" {
		if len(usageLines) > 1 {
			usageLines = append(usageLines, InfoPanelLineBg.Width(lineW).Render(""))
		}
		usageLines = append(usageLines, InfoPanelLineBg.Width(lineW).Render(InfoPanelDim.Render("TOKENS")))
		usageLines = append(usageLines, usageSummary)
		// Cache details only at tier 2+.
		if bp >= 2 {
			if cacheLine := renderUsageCacheLine(lineW, stats); cacheLine != "" {
				usageLines = append(usageLines, cacheLine)
			}
		}
	}
	if len(usageLines) <= 1 {
		return ""
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(usageLines))
}

func (m *Model) buildInfoPanelLSPBlock(lineW int) string {
	mcpErrStyle := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg))
	mcpErrLabelStyle := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg))
	pendingDotStyle := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg))

	var lspRows []agent.LSPServerDisplay
	if lp, ok := m.agent.(agent.LSPStateProvider); ok {
		lspRows = lp.LSPServerList()
	}
	if len(lspRows) == 0 {
		return ""
	}

	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionLSP)
	lines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "LSP", fmt.Sprintf("%d", len(lspRows)))}
	if !expanded {
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
	}
	// Note: LSP error details are intentionally hidden in the info panel; see logs for full cause.
	for _, row := range lspRows {
		var dot lipgloss.Style
		var labelStyle lipgloss.Style
		switch {
		case row.OK:
			dot = GaugeFull
			labelStyle = InfoPanelDim
		case row.Pending:
			dot = pendingDotStyle
			labelStyle = InfoPanelDim
		default:
			dot = mcpErrStyle
			labelStyle = mcpErrLabelStyle
		}
		contentWidth := lineW - infoPanelCollapsibleContentInset - 2
		if contentWidth < 1 {
			contentWidth = 1
		}
		label := row.Name
		diagSuffix := ""
		if row.OK {
			diagSuffix = formatLSPServerDiagSuffix(row.Errors, row.Warnings)
		}
		availLabel := contentWidth
		if diagSuffix != "" {
			availLabel -= lipgloss.Width(diagSuffix) + 1
		}
		if availLabel < 1 {
			availLabel = 1
		}
		line := lipgloss.JoinHorizontal(lipgloss.Left,
			dot.Render("●"),
			labelStyle.Render(" "+truncateOneLine(label, availLabel)),
		)
		if diagSuffix != "" {
			line = lipgloss.JoinHorizontal(lipgloss.Left, line, InfoPanelDim.Render(" "), renderLSPDiagSummary(diagSuffix))
		}
		lines = append(lines, renderInfoPanelCollapsibleContentLine(lineW, line))
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func (m *Model) buildInfoPanelMCPBlock(lineW int) string {
	mcpErrStyle := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg))
	mcpRetryLabelStyle := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelWarningFg))
	pendingDotStyle := InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg))

	var mcpRows []agent.MCPServerDisplay
	if mp, ok := m.agent.(agent.MCPStateProvider); ok {
		mcpRows = mp.MCPServerList()
	}
	if len(mcpRows) == 0 {
		return ""
	}

	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionMCP)
	lines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "MCP", fmt.Sprintf("%d", len(mcpRows)))}
	if !expanded {
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
	}
	for _, row := range mcpRows {
		var dot lipgloss.Style
		var labelStyle lipgloss.Style
		switch {
		case row.OK:
			dot = GaugeFull
			labelStyle = InfoPanelDim
		case row.Pending && row.Retrying:
			dot = pendingDotStyle
			labelStyle = mcpRetryLabelStyle
		case row.Pending:
			dot = pendingDotStyle
			labelStyle = InfoPanelDim
		default:
			dot = mcpErrStyle
			labelStyle = InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg))
		}
		contentWidth := lineW - infoPanelCollapsibleContentInset - 2
		if contentWidth < 1 {
			contentWidth = 1
		}
		line := lipgloss.JoinHorizontal(lipgloss.Left,
			dot.Render("●"),
			labelStyle.Render(" "+truncateOneLine(row.Name, contentWidth)),
		)
		lines = append(lines, renderInfoPanelCollapsibleContentLine(lineW, line))
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func (m *Model) buildInfoPanelTodoBlock(lineW int) string {
	todos := m.agent.GetTodos()
	if len(todos) == 0 {
		return ""
	}
	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionTodos)
	completed := 0
	for _, t := range todos {
		if t.Status == "completed" {
			completed++
		}
	}
	todoLines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "TODOS", fmt.Sprintf("%d/%d", completed, len(todos)))}
	if !expanded {
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(todoLines))
	}
	for _, t := range todos {
		var icon lipgloss.Style
		var marker string
		switch t.Status {
		case "completed":
			icon = GaugeFull
			marker = "✓"
		case "in_progress":
			icon = GaugeWarning
			marker = "▶"
		case "cancelled":
			icon = InfoPanelDim
			marker = "✗"
		default: // pending
			icon = InfoPanelDim
			marker = "○"
		}
		prefix := marker + " "
		contentWidth := lineW - infoPanelCollapsibleContentInset - lipgloss.Width(prefix)
		if contentWidth < 1 {
			contentWidth = 1
		}
		content := truncateInfoPanelLine(t.Content, contentWidth)
		todoLines = append(todoLines,
			renderInfoPanelCollapsibleContentLine(lineW,
				icon.Render(prefix)+InfoPanelDim.Render(content),
			),
		)
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(todoLines))
}

type infoPanelAgentRowHitBox struct {
	agentID   string
	startLine int
	endLine   int
}

func (m *Model) buildInfoPanelAgentListBlockWithHits(lineW int) (string, []infoPanelAgentRowHitBox) {
	if !m.sidebar.Visible() {
		return "", nil
	}
	agentRows := m.sidebar.buildInfoPanelRenderedLines(lineW - infoPanelCollapsibleContentInset - 2)
	if len(agentRows) == 0 {
		return "", nil
	}

	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionAgents)
	blockLines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "AGENTS", m.sidebar.AgentsSummary())}
	if !expanded {
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(blockLines)), nil
	}

	// Limit total agent lines to avoid dominating the panel.
	const maxAgentLines = 10
	totalLines := len(agentRows)
	truncated := totalLines > maxAgentLines
	if truncated {
		agentRows = agentRows[:maxAgentLines]
	}
	var rowHits []infoPanelAgentRowHitBox
	for _, row := range agentRows {
		lineIndex := len(blockLines)
		blockLines = append(blockLines, renderInfoPanelCollapsibleContentLine(lineW, row.Text))
		if row.AgentID != "" {
			rowHits = append(rowHits, infoPanelAgentRowHitBox{
				agentID:   row.AgentID,
				startLine: lineIndex,
				endLine:   lineIndex + 1,
			})
		}
	}
	if truncated {
		blockLines = append(blockLines, renderInfoPanelCollapsibleContentLine(lineW,
			InfoPanelDim.Render(fmt.Sprintf("+%d more", totalLines-maxAgentLines)),
		))
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(blockLines)), rowHits
}

func (m *Model) buildInfoPanelSkillsBlock(lineW int) string {
	type infoPanelSkillEntry struct {
		name        string
		description string
		invoked     bool
	}

	entries := make([]infoPanelSkillEntry, 0)
	indexByName := make(map[string]int)
	if sp, ok := m.agent.(agent.SkillsStateProvider); ok {
		for _, sk := range sp.ListSkills() {
			if sk == nil || strings.TrimSpace(sk.Name) == "" {
				continue
			}
			name := strings.TrimSpace(sk.Name)
			indexByName[name] = len(entries)
			entries = append(entries, infoPanelSkillEntry{
				name:        name,
				description: strings.TrimSpace(sk.Description),
			})
		}
	}
	for _, sk := range m.agent.InvokedSkills() {
		if sk == nil || strings.TrimSpace(sk.Name) == "" {
			continue
		}
		name := strings.TrimSpace(sk.Name)
		if idx, ok := indexByName[name]; ok {
			entries[idx].invoked = true
			if entries[idx].description == "" {
				entries[idx].description = strings.TrimSpace(sk.Description)
			}
			continue
		}
		indexByName[name] = len(entries)
		entries = append(entries, infoPanelSkillEntry{
			name:        name,
			description: strings.TrimSpace(sk.Description),
			invoked:     true,
		})
	}
	if len(entries) == 0 {
		return ""
	}
	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionSkills)
	skillLines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "SKILLS", fmt.Sprintf("%d", len(entries)))}
	if !expanded {
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(skillLines))
	}
	invokedStyle := InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelSuccessFg))
	for _, entry := range entries {
		lineStyle := InfoPanelDim
		if entry.invoked {
			lineStyle = invokedStyle
		}
		label := entry.name
		if entry.invoked && entry.description == "" {
			label += " (loaded)"
		}
		labelWidth := lineW - infoPanelCollapsibleContentInset
		if labelWidth < 1 {
			labelWidth = 1
		}
		skillLines = append(skillLines, renderInfoPanelCollapsibleContentLine(lineW, lineStyle.Render(truncateOneLine(label, labelWidth))))
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(skillLines))
}

func (m *Model) buildInfoPanelFilesBlock(lineW int) string {
	editedFiles := m.sidebar.CurrentAgentFiles()
	if len(editedFiles) == 0 {
		return ""
	}
	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionFiles)
	filesLines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "EDITED FILES", fmt.Sprintf("%d", len(editedFiles)))}
	if !expanded {
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(filesLines))
	}
	for _, fe := range editedFiles {
		baseName := filepath.Base(fe.Path)
		var parts string
		if fe.Added > 0 {
			parts += InfoPanelEditAddedStyle.Render(fmt.Sprintf("+%d", fe.Added))
		}
		if fe.Removed > 0 {
			if parts != "" {
				parts += InfoPanelDim.Render(" ")
			}
			parts += InfoPanelEditRemovedStyle.Render(fmt.Sprintf("-%d", fe.Removed))
		}
		statStr := parts
		availName := lineW - infoPanelCollapsibleContentInset
		if statStr != "" {
			availName -= lipgloss.Width(statStr) + 1
		}
		if availName < 1 {
			availName = 1
		}
		namePart := InfoPanelDim.Render(truncateOneLine(baseName, availName))
		var fileLine string
		if statStr != "" {
			fileLine = renderInfoPanelCollapsibleContentLine(lineW,
				lipgloss.JoinHorizontal(lipgloss.Left,
					namePart,
					InfoPanelDim.Render(" "),
					statStr,
				),
			)
		} else {
			fileLine = renderInfoPanelCollapsibleContentLine(lineW, namePart)
		}
		filesLines = append(filesLines, fileLine)
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(filesLines))
}

// contextValueStyle returns a style for the context usage value so it matches the gauge:
// normal (green) ≤50%, warning (orange) 50–80%, critical (red) >80%.
func contextValueStyle(percent float64) lipgloss.Style {
	if percent > 0.8 {
		return InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelCriticalFg))
	}
	if percent > 0.5 {
		return InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelWarningFg))
	}
	return InfoPanelValue
}

func renderInfoPanelKVLine(lineW int, key, value string) string {
	prefix := InfoPanelDim.Render(key + ": ")
	line := lipgloss.JoinHorizontal(lipgloss.Left, prefix, value)
	return InfoPanelLineBg.Width(lineW).Render(line)
}

func renderUsageSummaryLine(lineW int, stats analytics.SessionStats) string {
	parts := make([]string, 0, 3)
	if stats.InputTokens > 0 {
		parts = append(parts, InfoPanelDim.Render("↑ ")+InfoPanelValue.Render(formatUsageTokens(stats.InputTokens)))
	}
	if stats.OutputTokens > 0 {
		parts = append(parts, InfoPanelDim.Render("↓ ")+InfoPanelValue.Render(formatUsageTokens(stats.OutputTokens)))
	}
	if stats.EstimatedCost > 0 {
		parts = append(parts, InfoPanelDim.Render("$ ")+InfoPanelValue.Render(fmt.Sprintf("%.4f", stats.EstimatedCost)))
	}
	if len(parts) == 0 {
		return ""
	}
	return InfoPanelLineBg.Width(lineW).Render(strings.Join(parts, InfoPanelDim.Render("  ")))
}

func renderUsageCacheLine(lineW int, stats analytics.SessionStats) string {
	rows := make([]string, 0, 2)
	labelWidth := ansi.StringWidth("Cache W")
	if stats.CacheReadTokens > 0 {
		rows = append(rows, renderUsageCacheDetailLine(lineW, "Cache R", labelWidth,
			formatUsageCacheValue(stats.CacheReadTokens, stats.InputTokens)))
	}
	if stats.CacheWriteTokens > 0 {
		rows = append(rows, renderUsageCacheDetailLine(lineW, "Cache W", labelWidth,
			InfoPanelValue.Render(formatUsageTokens(stats.CacheWriteTokens))))
	}
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\n")
}

func renderUsageCacheDetailLine(lineW int, label string, labelWidth int, value string) string {
	pad := labelWidth - ansi.StringWidth(label)
	if pad < 0 {
		pad = 0
	}
	line := lipgloss.JoinHorizontal(lipgloss.Left,
		InfoPanelDim.Render(label),
		InfoPanelDim.Render(strings.Repeat(" ", pad+2)),
		value,
	)
	return InfoPanelLineBg.Width(lineW).Render(line)
}

func formatUsageCacheValue(cacheTokens, inputTokens int64) string {
	text := InfoPanelValue.Render(formatUsageTokens(cacheTokens))
	if inputTokens > 0 {
		text += InfoPanelDim.Render(" (") + InfoPanelValue.Render(formatPercent(float64(cacheTokens)/float64(inputTokens))) + InfoPanelDim.Render(")")
	}
	return text
}

func truncateInfoPanelLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

func (m *Model) renderContextGauge(width int, percent float64) string {
	if width <= 2 {
		return ""
	}
	innerWidth := width - 2
	fullSize := int(float64(innerWidth) * percent)
	if fullSize > innerWidth {
		fullSize = innerWidth
	}
	emptySize := innerWidth - fullSize

	style := GaugeFull
	if percent > 0.8 {
		style = GaugeCritical
	} else if percent > 0.5 {
		style = GaugeWarning
	}

	bracketStyle := GaugeEmpty
	return bracketStyle.Render("[") + style.Render(strings.Repeat("■", fullSize)) + GaugeEmpty.Render(strings.Repeat("□", emptySize)) + bracketStyle.Render("]")
}
