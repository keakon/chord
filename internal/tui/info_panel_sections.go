package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/keakon/lipgloss/v2"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/analytics"
	"github.com/keakon/chord/internal/bytefmt"
	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/skill"
	"github.com/keakon/chord/internal/textclean"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/modelref"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

const (
	infoPanelCollapsibleContentInset = 2
	// infoPanelEditedFilesHardLimit is a last-resort guard, not a display
	// budget: the info panel is scrollable, so every changed file the sidebar
	// still holds is rendered and reachable by scrolling rather than collapsed
	// into an overflow row. It reuses the sidebar's own cap so the two layers
	// cannot drift apart and truncate the same list twice.
	infoPanelEditedFilesHardLimit = sidebarMaxEditedFiles
)

func joinInfoPanelBlockLines(lines []string) string {
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return lines[0]
	default:
		var b strings.Builder
		total := len(lines) - 1
		for _, line := range lines {
			total += len(line)
		}
		b.Grow(total)
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

func renderInfoPanelIndentedLine(lineW, inset int, content string) string {
	if inset < 0 {
		inset = 0
	}
	prefix := strings.Repeat(" ", inset)
	line := lipgloss.JoinHorizontal(lipgloss.Left, currentStyles.InfoPanelDim.Render(prefix), content)
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(line)
}

func (m *Model) renderInfoPanelServiceTierLine(lineW int) string {
	state := m.focusedModelState()
	requested := state.ServiceTier
	if requested == config.ServiceTierStandard {
		return ""
	}
	label := currentStyles.InfoPanelValue.Render("tier: ")
	valueStyle := currentStyles.InfoPanelValue
	if requested != state.EffectiveTier {
		valueStyle = currentStyles.InfoPanelDim.Strikethrough(true)
	}
	line := lipgloss.JoinHorizontal(lipgloss.Left, label, valueStyle.Render(string(requested)))
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(line)
}

// buildInfoPanelBackendBlock names the external backend that owns the model.
// Chord has no model identity, usage, or timing for that process, so the
// backend kind replaces MODEL instead of being presented as a model.
func (m *Model) buildInfoPanelBackendBlock(lineW int) string {
	backend := strings.TrimSpace(m.focusedModelState().Backend)
	if backend == "" {
		return ""
	}
	lines := []string{
		currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelTitle.Render("BACKEND")),
		currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelValue.Render(terminaltext.TruncateOneLine(backend, lineW))),
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func (m *Model) buildInfoPanelModelBlock(lineW int) string {
	modelState := m.focusedModelState()
	if strings.TrimSpace(modelState.Backend) != "" {
		// The external CLI picks and calls its own model; the BACKEND section
		// names the backend instead. Omit MODEL entirely rather than render a
		// placeholder or leak the main agent's model.
		return ""
	}
	provider, model, variant := modelref.SplitRunningModelRef(modelState.DisplayRef)
	modelShown := modelref.FormatModelVariantForDisplay(model, variant, lineW)
	modelLines := []string{
		currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelTitle.Render("MODEL")),
		currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelValue.Render(modelShown)),
	}
	if provider != "" {
		modelLines = append(modelLines,
			renderInfoPanelKVLine(lineW, "Provider", currentStyles.InfoPanelValue.Render(terminaltext.TruncateOneLine(provider, lineW-10))),
		)
	}
	keysConfirmed, keysTotal := modelState.KeysConfirmed, modelState.KeysTotal
	keysStyle := currentStyles.InfoPanelValue
	if keysTotal > 1 {
		switch keyPoolHealthSeverity(keysConfirmed, keysTotal) {
		case keyPoolSeverityCritical:
			keysStyle = currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelKeyCriticalFg))
		case keyPoolSeverityWarn:
			keysStyle = currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelKeyWarnFg))
		}
	}
	if len(modelState.PoolNames) > 0 {
		if pool := strings.TrimSpace(modelState.PoolName); pool != "" {
			pool = m.pendingPoolSwitch.display(pool, m.isFocusedAgentBusy())
			modelLines = append(modelLines, renderInfoPanelKVLine(lineW, "Pool", currentStyles.InfoPanelValue.Render(pool)))
		}
	}
	if keysTotal > 1 {
		keysStr := fmt.Sprintf("Keys: %d/%d", keysConfirmed, keysTotal)
		modelLines = append(modelLines, currentStyles.InfoPanelLineBg.Width(lineW).Render(keysStyle.Render(keysStr)))
	}
	if tierLine := m.renderInfoPanelServiceTierLine(lineW); tierLine != "" {
		modelLines = append(modelLines, tierLine)
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(modelLines))
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
	if m.focusedModelState().Backend != "" {
		// Context and token usage belong to a model Chord sent requests to.
		return ""
	}
	// current is the usage-only reading the auto-compaction decision compares
	// against its threshold: the last observed post-response baseline, the
	// single frozen estimate when the latest response missed usage, or 0 when
	// unknown. Unknown is what a session that has not called the model yet
	// looks like, so it renders as a plain 0 with no extra mark and never
	// triggers; the frozen estimate is computed rather than observed and is
	// marked with ≈. A model/window change keeps the previous window's reading
	// on the gauge until a new response replaces it, marked with the same ≈
	// because it no longer describes the current model's window. Post-response growth
	// never moves the value, so the gauge reads the same pressure the trigger
	// does, except that a stale reading is display-only and never triggers.
	// percent = current / usable input budget.
	// Color the Context value and its gauge from the focused agent's pressure
	// lines: orange once usage reaches the reminder line, red once it reaches
	// the auto-compaction threshold. Agents without usage-driven lines (focused
	// SubAgent, parked target) fall back to the fixed 50/80% lines.
	current, limit := m.agent.GetContextStats()
	usageState := m.agent.GetContextUsageState()
	percent := 0.0
	if limit > 0 {
		percent = float64(current) / float64(limit)
	}
	reminder, threshold := m.contextPressureLinesForFocusedModel()
	reduction := m.agent.GetContextReductionStats()
	currentBytes := m.agent.GetContextBytes()
	if reduction.CurrentBytes > 0 {
		currentBytes = reduction.CurrentBytes
	}
	msgCount := m.agent.GetContextMessageCount()
	if reduction.CurrentMessages > 0 {
		msgCount = reduction.CurrentMessages
	}

	stats := m.agent.GetSidebarUsageStats()
	usageLines := []string{currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelTitle.Render("USAGE"))}
	if current > 0 || limit > 0 {
		gauge := m.renderContextGauge(width-6, percent, reminder, threshold)
		contextValueStr := fmt.Sprintf("%s (%s)", formatTokens(current), formatPercent(percent))
		if usageState == ctxmgr.ContextUsageEstimated || usageState == ctxmgr.ContextUsageStale {
			contextValueStr = "≈" + contextValueStr
		}
		usageLines = append(usageLines,
			renderInfoPanelKVLine(lineW, "Context", contextValueStyle(percent, reminder, threshold).Render(contextValueStr)),
			currentStyles.InfoPanelLineBg.Width(lineW).Render(gauge),
		)
		if currentBytes > 0 && msgCount > 0 {
			bytesValue := bytefmt.Short(int64(currentBytes))
			if reduction.Bytes > 0 {
				requestBefore := currentBytes + reduction.Bytes
				if requestBefore > 0 {
					// Hide negligible savings: a sub-1% ratio would round to
					// "(↓0%)", which looks like a reduction the user cannot perceive.
					if ratio := float64(reduction.Bytes) / float64(requestBefore); ratio >= 0.01 {
						bytesValue = formatReductionPercentValue(bytesValue, formatPercent(ratio))
					}
				}
			}
			usageLines = append(usageLines, renderInfoPanelKVLine(lineW, "Bytes", currentStyles.InfoPanelValue.Render(bytesValue)))
		}
		if msgCount >= 0 {
			messagesValue := formatTokens(msgCount)
			usageLines = append(usageLines, renderInfoPanelKVLine(lineW, "Messages", currentStyles.InfoPanelValue.Render(messagesValue)))
		}
	}

	if usageSummary := renderUsageSummaryLine(lineW, stats); usageSummary != "" {
		if len(usageLines) > 1 {
			usageLines = append(usageLines, currentStyles.InfoPanelLineBg.Width(lineW).Render(""))
		}
		usageLines = append(usageLines, currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelDim.Render("TOKENS")))
		usageLines = append(usageLines, usageSummary)
		if reasoningLine := renderUsageReasoningLine(lineW, stats); reasoningLine != "" {
			usageLines = append(usageLines, reasoningLine)
		}
		if cacheLine := renderUsageCacheLine(lineW, stats); cacheLine != "" {
			usageLines = append(usageLines, cacheLine)
		}
		if callsLine := renderUsageCallsLine(lineW, stats); callsLine != "" {
			usageLines = append(usageLines, callsLine)
		}
		if costLine := renderUsageCostLine(lineW, stats); costLine != "" {
			usageLines = append(usageLines, costLine)
		}
	}
	if len(usageLines) <= 1 {
		return ""
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(usageLines))
}

func (m *Model) buildInfoPanelTimeBlock(lineW int) string {
	if m.focusedModelState().Backend != "" {
		// Chord does not schedule or time the external process's work.
		return ""
	}
	stats := m.agent.GetSidebarWalltimeStats()
	if stats.IsZero() {
		// No walltime recorded for the focused agent: hide the whole section.
		return ""
	}
	lines := []string{currentStyles.InfoPanelLineBg.Width(lineW).Render(currentStyles.InfoPanelTitle.Render("TIME"))}

	// Buckets below one second are not shown: a sub-second tool call rendered
	// as "0s (1%)" is misleading, and its share is not worth a percentage.
	// They are excluded from the total, so the displayed buckets always sum
	// to 100.
	type bucketRow struct {
		key string
		dur time.Duration
	}
	rows := make([]bucketRow, 0, 4)
	if stats.Model >= time.Second {
		rows = append(rows, bucketRow{"Model", stats.Model})
	}
	if stats.Tool >= time.Second {
		rows = append(rows, bucketRow{"Tools", stats.Tool})
	}
	if stats.Cooldown >= time.Second {
		rows = append(rows, bucketRow{"Cooldown", stats.Cooldown})
	}
	if stats.UserWait >= time.Second {
		rows = append(rows, bucketRow{"User wait", stats.UserWait})
	}
	if len(rows) == 0 {
		// Every bucket is sub-second: nothing worth showing.
		return ""
	}
	var total time.Duration
	for _, row := range rows {
		total += row.dur
	}
	// Allocate integer percentages so the buckets always sum to 100 and never
	// go negative, even under an extreme skew (e.g. one bucket dominating
	// while three sit at ~1s). Largest-remainder distribution: floor each
	// share first, then hand out the leftover units to the largest fractional
	// remainders one at a time.
	exact := make([]float64, len(rows))
	base := make([]int, len(rows))
	rem := 100
	totalF := float64(total)
	for i, row := range rows {
		exact[i] = float64(row.dur) / totalF * 100
		base[i] = int(exact[i])
		rem -= base[i]
	}
	for rem > 0 {
		best := 0
		for i := 1; i < len(rows); i++ {
			if exact[i]-float64(base[i]) > exact[best]-float64(base[best]) {
				best = i
			}
		}
		base[best]++
		rem--
	}
	for i, row := range rows {
		value := formatWalltimeValue(lineW, row.key, row.dur, base[i])
		lines = append(lines, renderInfoPanelKVLine(lineW, row.key, currentStyles.InfoPanelValue.Render(value)))
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func formatWalltimeValue(lineW int, key string, duration time.Duration, percent int) string {
	available := lineW - ansi.StringWidth(key+": ")
	if available <= 0 {
		return ""
	}

	durations := walltimeDurationVariants(duration)
	for _, elapsed := range durations {
		value := fmt.Sprintf("%s (%d%%)", elapsed, percent)
		if ansi.StringWidth(value) <= available {
			return value
		}
	}

	// Keep the value meaningful even for a panel too narrow for the normal
	// duration/percentage pair; never add an ellipsis to the TIME section.
	percentText := fmt.Sprintf("%d%%", percent)
	if ansi.StringWidth(percentText) <= available {
		return percentText
	}
	shortest := durations[len(durations)-1]
	if ansi.StringWidth(shortest) <= available {
		return shortest
	}
	return ""
}

func walltimeDurationVariants(d time.Duration) []string {
	sec := int(d.Seconds())
	variants := []string{tools.FormatElapsed(d)}
	appendVariant := func(value string) {
		if value != variants[len(variants)-1] {
			variants = append(variants, value)
		}
	}
	if sec >= 60 {
		if sec >= 60*60 {
			hours := sec / (60 * 60)
			minutes := sec / 60 % 60
			withoutSeconds := fmt.Sprintf("%dh", hours)
			if minutes > 0 {
				withoutSeconds = fmt.Sprintf("%dh%02dm", hours, minutes)
			}
			appendVariant(withoutSeconds)
			appendVariant(fmt.Sprintf("%dh", hours))
		} else {
			appendVariant(fmt.Sprintf("%dm", sec/60))
		}
	}
	return variants
}

func (m *Model) buildInfoPanelLSPBlock(lineW int, integrations infoPanelIntegrations) string {
	mcpErrStyle := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg))
	mcpErrLabelStyle := currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg))
	pendingDotStyle := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelPendingFg))

	lspRows := integrations.lsp
	if len(lspRows) == 0 {
		return ""
	}

	expanded := !m.infoPanelSectionCollapsed(infoPanelSectionLSP, integrations.collapsed(infoPanelSectionLSP))
	failed := 0
	for _, row := range lspRows {
		if !row.OK && !row.Idle && !row.Pending {
			failed++
		}
	}
	lines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "LSP", integrationHeaderSummary(len(lspRows), failed))}
	if !expanded {
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
	}
	// Note: LSP error details are intentionally hidden in the info panel; see logs for full cause.
	for _, row := range lspRows {
		var dot lipgloss.Style
		var labelStyle lipgloss.Style
		switch {
		case row.OK:
			dot = currentStyles.GaugeFull
			labelStyle = currentStyles.InfoPanelDim
		case row.Idle:
			dot = pendingDotStyle
			labelStyle = currentStyles.InfoPanelDim
		case row.Pending:
			dot = pendingDotStyle
			labelStyle = currentStyles.InfoPanelDim
		default:
			dot = mcpErrStyle
			labelStyle = mcpErrLabelStyle
		}
		contentWidth := max(lineW-infoPanelCollapsibleContentInset-2, 1)
		label := lspWorkspaceLabel(row, lspRows)
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
			labelStyle.Render(" "+terminaltext.TruncateOneLine(label, availLabel)),
		)
		if diagSuffix != "" {
			line = lipgloss.JoinHorizontal(lipgloss.Left, line, currentStyles.InfoPanelDim.Render(" "), renderLSPDiagSummary(diagSuffix))
		}
		lines = append(lines, renderInfoPanelCollapsibleContentLine(lineW, line))
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func (m *Model) buildInfoPanelMCPBlock(lineW int, integrations infoPanelIntegrations) string {
	mcpErrStyle := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg))
	mcpRetryLabelStyle := currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg))
	pendingDotStyle := currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelPendingFg))

	mcpRows := integrations.mcp
	if len(mcpRows) == 0 {
		return ""
	}

	expanded := !m.infoPanelSectionCollapsed(infoPanelSectionMCP, integrations.collapsed(infoPanelSectionMCP))
	failed := 0
	for _, row := range mcpRows {
		if !row.OK && !row.Idle && !row.Pending && !row.Disabled {
			failed++
		}
	}
	lines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "MCP", integrationHeaderSummary(len(mcpRows), failed))}
	if !expanded {
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
	}
	for _, row := range mcpRows {
		var dot lipgloss.Style
		var labelStyle lipgloss.Style
		switch {
		case row.OK:
			dot = currentStyles.GaugeFull
			labelStyle = currentStyles.InfoPanelDim
		case row.Idle:
			// Auto-unloaded while idle: dim gray, not an error.
			dot = pendingDotStyle
			labelStyle = currentStyles.InfoPanelDim
		case row.Disabled:
			// Disabled/manual: dim gray.
			dot = pendingDotStyle
			labelStyle = currentStyles.InfoPanelDim
		case row.Pending && row.Retrying:
			// Retrying: warning/orange.
			dot = currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg))
			labelStyle = mcpRetryLabelStyle
		case row.Pending:
			// Connecting: keep dim label (tests pin this); use a warning-colored dot.
			dot = currentStyles.InfoPanelDim.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg))
			labelStyle = currentStyles.InfoPanelDim
		default:
			dot = mcpErrStyle
			labelStyle = currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg))
		}
		contentWidth := max(lineW-infoPanelCollapsibleContentInset-2, 1)
		line := lipgloss.JoinHorizontal(lipgloss.Left,
			dot.Render("●"),
			labelStyle.Render(" "+terminaltext.TruncateOneLine(row.Name, contentWidth)),
		)
		lines = append(lines, renderInfoPanelCollapsibleContentLine(lineW, line))
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func (m *Model) buildInfoPanelGitBlock(lineW int) string {
	info := m.gitStatus.Info
	if !info.Present {
		return ""
	}
	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionGit)
	headerSummary := ""
	if !expanded {
		headerSummary = gitStatusSummaryBudgeted(info, infoPanelCollapsibleSummaryBudget(lineW, expanded, "GIT"))
	}
	lines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "GIT", headerSummary)}
	if !expanded {
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
	}
	ref := info.Branch
	if ref == "" {
		ref = info.Commit
	}
	if ref == "" {
		ref = "detached"
	}
	lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Branch", currentStyles.InfoPanelValue.Render(terminaltext.TruncateOneLine(ref, lineW-8-infoPanelCollapsibleContentInset))))
	if info.WorktreeName != "" {
		lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Worktree", currentStyles.InfoPanelValue.Render(terminaltext.TruncateOneLine(info.WorktreeName, lineW-10-infoPanelCollapsibleContentInset))))
		// The status bar renders the worktree's identity label instead of its
		// path, so the panel is where that path stays visible — but the name
		// above comes from the git snapshot, while the path is the live
		// directory. Only show the path while the snapshot still describes that
		// directory, so a stale name is never paired with a new checkout.
		if info.Dir != "" && info.Dir == m.workingDir {
			lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Worktree Path", currentStyles.InfoPanelValue.Render(terminaltext.TruncateOneLine(displayWorkingDir(m.workingDir), lineW-16-infoPanelCollapsibleContentInset))))
		}
	}
	if info.ConflictedFiles > 0 {
		lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Conflicts", currentStyles.InfoPanelValue.Render(fmt.Sprintf("%d files", info.ConflictedFiles))))
	}
	if info.ChangedFiles > 0 {
		lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Changes", currentStyles.InfoPanelValue.Render(fmt.Sprintf("%d files", info.ChangedFiles))))
	}
	if info.StagedFiles > 0 {
		lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Staged", currentStyles.InfoPanelValue.Render(fmt.Sprintf("%d files", info.StagedFiles))))
	}
	if info.Stashes > 0 {
		lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Stash", currentStyles.InfoPanelValue.Render(fmt.Sprintf("%d entries", info.Stashes))))
	}
	syncParts := make([]string, 0, 2)
	if info.Ahead > 0 {
		syncParts = append(syncParts, fmt.Sprintf("↑%d", info.Ahead))
	}
	if info.Behind > 0 {
		syncParts = append(syncParts, fmt.Sprintf("↓%d", info.Behind))
	}
	if len(syncParts) > 0 {
		lines = append(lines, renderInfoPanelIndentedKVLine(lineW, infoPanelCollapsibleContentInset, "Sync", currentStyles.InfoPanelValue.Render(strings.Join(syncParts, " "))))
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
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
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(todoLines))
	}
	for _, t := range todos {
		var icon lipgloss.Style
		var marker string
		switch t.Status {
		case "completed":
			icon = currentStyles.GaugeFull
			marker = "✓"
		case "in_progress":
			icon = currentStyles.GaugeWarning
			marker = "▶"
		case "cancelled":
			icon = currentStyles.InfoPanelDim
			marker = "✗"
		default: // pending
			icon = currentStyles.InfoPanelDim
			marker = "○"
		}
		prefix := marker + " "
		contentWidth := max(lineW-infoPanelCollapsibleContentInset-lipgloss.Width(prefix), 1)
		content := truncateInfoPanelLine(t.Content, contentWidth)
		todoLines = append(todoLines,
			renderInfoPanelCollapsibleContentLine(lineW,
				icon.Render(prefix)+currentStyles.InfoPanelDim.Render(content),
			),
		)
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(todoLines))
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
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(blockLines)), nil
	}

	// Every agent the sidebar still tracks is rendered: the info panel scrolls,
	// so a display budget here would only make rows unreachable. The agent count
	// is already bounded upstream by delegation.max_children, so this cannot
	// grow without limit. See infoPanelEditedFilesHardLimit for the same rule
	// applied to CHANGED FILES.
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
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(blockLines)), rowHits
}

func (m *Model) buildInfoPanelSkillsBlock(lineW int, integrations infoPanelIntegrations) string {
	type infoPanelSkillEntry struct {
		name       string
		invocation skill.InvocationState
	}

	entries := make([]infoPanelSkillEntry, 0)
	indexByName := make(map[string]int)
	for _, sk := range integrations.skills {
		if sk == nil || strings.TrimSpace(sk.Name) == "" {
			continue
		}
		name := strings.TrimSpace(sk.Name)
		indexByName[name] = len(entries)
		// Without live invocation states the metadata flags are all there
		// is: manual-only and invoked are frontmatter-settable, and every
		// other state falls back to the idle icon. The state provider
		// below replaces this wholesale whenever it is available.
		entries = append(entries, infoPanelSkillEntry{
			name: name,
			invocation: skill.InvocationState{
				Meta:          sk,
				ModelCallable: !sk.DisableModelInvocation,
				BodyPresent:   sk.Invoked,
				UserLoadable:  true,
			},
		})
	}
	for _, sk := range integrations.invoked {
		if sk == nil || strings.TrimSpace(sk.Name) == "" {
			continue
		}
		if idx, ok := indexByName[strings.TrimSpace(sk.Name)]; ok {
			entries[idx].invocation.BodyPresent = true
			continue
		}
	}
	for _, state := range integrations.states {
		if state.Meta == nil {
			continue
		}
		if index, exists := indexByName[strings.TrimSpace(state.Meta.Name)]; exists {
			entries[index].invocation = state
		}
	}
	if len(entries) == 0 {
		return ""
	}
	expanded := !m.infoPanelSectionCollapsed(infoPanelSectionSkills, integrations.collapsed(infoPanelSectionSkills))
	skillLines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "SKILLS", fmt.Sprintf("%d", len(entries)))}
	if !expanded {
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(skillLines))
	}
	for _, entry := range entries {
		// The icon is the only status channel: shape carries the state
		// class, color the accent (green = body live, amber = reload after
		// compaction). The name stays dim so a row reads as a labeled
		// status light rather than a shout. Truncation runs on the plain
		// "glyph name" pair, so it counts the glyph instead of eating it.
		//
		// The separator lives inside the name's fragment rather than between
		// the two: a rendered fragment ends with a reset, and the panel's own
		// background is written once for the whole line, so a bare separator
		// would carry no background and show as a one-cell stripe on the
		// terminal's own background. Every other row in the panel keeps its
		// separators inside a styled fragment for the same reason.
		glyph, glyphStyle := skillStateIcon(entry.invocation)
		labelWidth := max(lineW-infoPanelCollapsibleContentInset, 1)
		glyphPart, namePart, _ := strings.Cut(terminaltext.TruncateOneLine(glyph+" "+entry.name, labelWidth), " ")
		row := glyphStyle.Render(glyphPart) + currentStyles.InfoPanelDim.Render(" "+namePart)
		skillLines = append(skillLines, renderInfoPanelCollapsibleContentLine(lineW, row))
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(skillLines))
}

func (m *Model) infoPanelSkills() []*skill.Meta {
	if sp, ok := m.agent.(agent.FocusedSkillsStateProvider); ok {
		return sp.FocusedSkills()
	}
	if sp, ok := m.agent.(agent.SkillsStateProvider); ok {
		return sp.UserSkills()
	}
	return nil
}

func (m *Model) buildInfoPanelFilesBlock(lineW int) string {
	editedFiles := m.sidebar.CurrentAgentFiles()
	if len(editedFiles) == 0 {
		return ""
	}
	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionFiles)
	filesLines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "CHANGED FILES", fmt.Sprintf("%d", len(editedFiles)))}
	if !expanded {
		return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(filesLines))
	}
	// Newest first: the panel answers "what just changed", so the most recent
	// edits belong at the eye's entry point rather than below the fold. Storage
	// stays oldest-first, including when an existing file changes again, so a
	// render-time reversal is enough here.
	//
	// A side effect worth keeping: the sidebar drops the oldest entries when it
	// hits its cap, and rendering oldest-last puts that eviction at the tail,
	// off-screen, instead of silently deleting rows from the top of the list.
	visibleFiles := editedFiles
	if len(visibleFiles) > infoPanelEditedFilesHardLimit {
		visibleFiles = visibleFiles[len(visibleFiles)-infoPanelEditedFilesHardLimit:]
	}
	for _, fe := range slices.Backward(visibleFiles) {
		baseName := filepath.Base(fe.Path)
		var parts string
		if !fe.Deleted {
			if fe.Added > 0 {
				parts += currentStyles.InfoPanelEditAddedStyle.Render(fmt.Sprintf("+%d", fe.Added))
			}
			if fe.Removed > 0 {
				if parts != "" {
					parts += currentStyles.InfoPanelDim.Render(" ")
				}
				parts += currentStyles.InfoPanelEditRemovedStyle.Render(fmt.Sprintf("-%d", fe.Removed))
			}
		}
		statStr := parts
		contentW := max(lineW-infoPanelCollapsibleContentInset, 0)
		availName := contentW
		if statStr != "" {
			availName -= lipgloss.Width(statStr)
			if availName > 0 {
				availName--
			}
			// Keep a spare trailing cell after the stats so the final digit does not
			// land on the panel edge and get clipped by the screen compositor.
			if availName > 0 {
				availName--
			}
		}
		nameStyle := currentStyles.InfoPanelDim
		if fe.Deleted {
			nameStyle = nameStyle.Strikethrough(true)
		}
		namePart := ""
		if availName > 0 {
			namePart = nameStyle.Render(terminaltext.TruncateOneLine(baseName, availName))
		}
		var fileLine string
		if statStr != "" && namePart != "" {
			fileLine = renderInfoPanelCollapsibleContentLine(lineW,
				lipgloss.JoinHorizontal(lipgloss.Left,
					namePart,
					currentStyles.InfoPanelDim.Render(" "),
					statStr,
				),
			)
		} else if statStr != "" {
			fileLine = renderInfoPanelCollapsibleContentLine(lineW, statStr)
		} else {
			fileLine = renderInfoPanelCollapsibleContentLine(lineW, namePart)
		}
		filesLines = append(filesLines, fileLine)
	}
	if hidden := len(editedFiles) - len(visibleFiles); hidden > 0 {
		filesLines = append(filesLines, renderInfoPanelCollapsibleContentLine(lineW,
			currentStyles.InfoPanelDim.Render(fmt.Sprintf("… and %d more", hidden)),
		))
	}
	return currentStyles.InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(filesLines))
}

// Fixed fallback lines for context usage display when the focused agent does
// not expose reminder/threshold lines (a focused SubAgent, whose context is
// managed by sliding-window compaction, or a parked target): orange past 50%,
// red past 80% — the historical fixed behavior.
const (
	infoPanelContextFallbackWarnRatio     = 0.5
	infoPanelContextFallbackCriticalRatio = 0.8
)

type contextUsageSeverity uint8

const (
	contextUsageNormal contextUsageSeverity = iota
	contextUsageWarning
	contextUsageCritical
)

// contextUsageSeverityFor maps a context usage ratio onto the display severity
// shared by the Context value and its gauge. Critical means usage reached the
// agent's auto-compaction threshold; warning means it reached the reminder
// line (a reminder configured at or above the threshold simply never shows
// warning — the threshold crossing is critical). Agents without usage-driven
// lines report 0 for both and fall back to the fixed 50%/80% lines. A 0
// threshold (auto-compaction disabled) keeps the fixed 80% line as an
// oversize-risk guard, since no compaction protects the model there.
func contextUsageSeverityFor(percent, reminder, threshold float64) contextUsageSeverity {
	if threshold <= 0 {
		threshold = infoPanelContextFallbackCriticalRatio
	}
	if reminder <= 0 {
		reminder = infoPanelContextFallbackWarnRatio
	}
	switch {
	case percent >= threshold:
		return contextUsageCritical
	case percent >= reminder:
		return contextUsageWarning
	default:
		return contextUsageNormal
	}
}

// contextValueStyle returns a style for the context usage value so it matches
// the gauge: normal (green) below the reminder line, warning (orange) from the
// reminder line up to the auto-compaction threshold, critical (red) at the
// threshold.
func contextValueStyle(percent, reminder, threshold float64) lipgloss.Style {
	switch contextUsageSeverityFor(percent, reminder, threshold) {
	case contextUsageCritical:
		return currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelCriticalFg))
	case contextUsageWarning:
		return currentStyles.InfoPanelValue.Foreground(lipgloss.Color(currentStyles.Palette.InfoPanelWarningFg))
	default:
		return currentStyles.InfoPanelValue
	}
}

func renderInfoPanelIndentedKVLine(lineW, inset int, key, value string) string {
	prefix := currentStyles.InfoPanelDim.Render(key + ": ")
	line := lipgloss.JoinHorizontal(lipgloss.Left, prefix, value)
	return renderInfoPanelIndentedLine(lineW, inset, line)
}

func formatReductionPercentValue(current, percent string) string {
	return fmt.Sprintf("%s (↓%s)", current, percent)
}

func renderInfoPanelKVLine(lineW int, key, value string) string {
	prefix := currentStyles.InfoPanelDim.Render(key + ": ")
	line := lipgloss.JoinHorizontal(lipgloss.Left, prefix, value)
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(line)
}

func renderUsageSummaryLine(lineW int, stats analytics.SessionStats) string {
	parts := make([]string, 0, 2)
	fullInputTokens := analytics.FullInputTokens(stats.InputTokens, stats.CacheReadTokens, stats.CacheWriteTokens)
	if fullInputTokens > 0 {
		parts = append(parts, currentStyles.InfoPanelDim.Render("↑ ")+currentStyles.InfoPanelValue.Render(formatUsageTokens(fullInputTokens)))
	}
	if stats.OutputTokens > 0 {
		parts = append(parts, currentStyles.InfoPanelDim.Render("↓ ")+currentStyles.InfoPanelValue.Render(formatUsageTokens(stats.OutputTokens)))
	}
	if len(parts) == 0 {
		return ""
	}
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(strings.Join(parts, currentStyles.InfoPanelDim.Render("  ")))
}

// usageDetailLabelWidth aligns the label column of the usage detail lines:
// every detail line donates its width from the widest fixed label.
const usageDetailLabelWidth = "Uncached"

func renderUsageCallsLine(lineW int, stats analytics.SessionStats) string {
	if stats.LLMCalls <= 0 {
		return ""
	}
	return renderUsageCacheDetailLine(lineW, "Calls", ansi.StringWidth(usageDetailLabelWidth), currentStyles.InfoPanelValue.Render(strconv.FormatInt(stats.LLMCalls, 10)))
}

func renderUsageCostLine(lineW int, stats analytics.SessionStats) string {
	if stats.EstimatedCost <= 0 {
		return ""
	}
	line := currentStyles.InfoPanelDim.Render("$ ") + currentStyles.InfoPanelValue.Render(fmt.Sprintf("%.4f", stats.EstimatedCost))
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(line)
}

func renderUsageReasoningLine(lineW int, stats analytics.SessionStats) string {
	if stats.ReasoningTokens <= 0 {
		return ""
	}
	return renderUsageCacheDetailLine(lineW, "Think", ansi.StringWidth(usageDetailLabelWidth), currentStyles.InfoPanelValue.Render(formatUsageTokens(stats.ReasoningTokens)))
}

func renderUsageCacheLine(lineW int, stats analytics.SessionStats) string {
	rows := make([]string, 0, 3)
	labelWidth := ansi.StringWidth(usageDetailLabelWidth)
	if stats.CacheReadTokens > 0 {
		rows = append(rows, renderUsageCacheDetailLine(lineW, "Cache R", labelWidth,
			formatUsageCacheValue(stats.CacheReadTokens, analytics.FullInputTokens(stats.InputTokens, stats.CacheReadTokens, stats.CacheWriteTokens))))
	}
	if stats.CacheWriteTokens > 0 {
		rows = append(rows, renderUsageCacheDetailLine(lineW, "Cache W", labelWidth,
			currentStyles.InfoPanelValue.Render(formatUsageTokens(stats.CacheWriteTokens))))
	}
	if len(rows) == 0 {
		return ""
	}
	return strings.Join(rows, "\n")
}

func renderUsageCacheDetailLine(lineW int, label string, labelWidth int, value string) string {
	pad := max(labelWidth-ansi.StringWidth(label), 0)
	line := lipgloss.JoinHorizontal(lipgloss.Left,
		currentStyles.InfoPanelDim.Render(label),
		currentStyles.InfoPanelDim.Render(strings.Repeat(" ", pad+2)),
		value,
	)
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(line)
}

func formatUsageCacheValue(cacheTokens, promptSideTokens int64) string {
	text := currentStyles.InfoPanelValue.Render(formatUsageTokens(cacheTokens))
	if promptSideTokens > 0 {
		// Hide negligible shares like "(0%)": the absolute token count stays,
		// only the percent suffix is omitted until it reaches 1%.
		if ratio := float64(cacheTokens) / float64(promptSideTokens); ratio >= 0.01 {
			text += currentStyles.InfoPanelDim.Render(" (") + currentStyles.InfoPanelValue.Render(formatPercent(ratio)) + currentStyles.InfoPanelDim.Render(")")
		}
	}
	return text
}

func truncateInfoPanelLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = textclean.NormalizeNewlines(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

func (m *Model) renderContextGauge(width int, percent, reminder, threshold float64) string {
	if width <= 2 {
		return ""
	}
	innerWidth := width - 2
	fullSize := min(int(float64(innerWidth)*percent), innerWidth)
	emptySize := innerWidth - fullSize

	style := currentStyles.GaugeFull
	switch contextUsageSeverityFor(percent, reminder, threshold) {
	case contextUsageCritical:
		style = currentStyles.GaugeCritical
	case contextUsageWarning:
		style = currentStyles.GaugeWarning
	}

	bracketStyle := currentStyles.GaugeEmpty
	return bracketStyle.Render("[") + style.Render(strings.Repeat("■", fullSize)) + currentStyles.GaugeEmpty.Render(strings.Repeat("□", emptySize)) + bracketStyle.Render("]")
}
