package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/modelref"
)

func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

func formatPercent(p float64) string {
	return fmt.Sprintf("%.0f%%", p*100)
}

func formatCost(cost float64) string {
	if cost == 0 {
		return "$0.00"
	}
	if cost < 0.01 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

func (m *Model) appendStatusBarModelPills(pills []string, snap statusBarAgentSnapshot, effectiveWidth, leftWidth int) []string {
	modelRef := snap.modelRef
	modelVariant := snap.modelVariant

	const modelPillPrefixRunes = 2
	modelSlotMax := effectiveWidth - leftWidth - modelPillPrefixRunes - 1
	if modelSlotMax < 8 {
		modelSlotMax = 8
	}

	var modelPill string
	if m.cachedModelPillRef == modelRef && m.cachedModelPillVariant == modelVariant &&
		m.cachedModelPillEffW == effectiveWidth && m.cachedModelPillLeftW == leftWidth {
		modelPill = m.cachedModelPill
	} else {
		modelStr := "unknown"
		if modelRef != "" {
			modelStr = modelref.FormatRunningModelRefForDisplay(modelRef, m.agent.ProviderModelRef(), modelVariant, modelSlotMax)
		}
		modelPill = PillStyle.Render("◇ " + modelStr)
		m.cachedModelPillRef = modelRef
		m.cachedModelPillVariant = modelVariant
		m.cachedModelPillEffW = effectiveWidth
		m.cachedModelPillLeftW = leftWidth
		m.cachedModelPill = modelPill
	}
	pills = append(pills, modelPill)

	if snap.proxyInUse {
		pills = append(pills, PillStyle.Render("↗"))
	}
	if snap.mcpPill != "" {
		pills = append(pills, snap.mcpPill)
	}

	usage := snap.tokenUsage
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		tokenParts := make([]string, 0, 2)
		if usage.InputTokens != 0 {
			tokenParts = append(tokenParts, "↑ "+formatTokens(usage.InputTokens))
		}
		if usage.OutputTokens != 0 {
			tokenParts = append(tokenParts, "↓ "+formatTokens(usage.OutputTokens))
		}
		pills = append(pills, PillStyle.Render(strings.Join(tokenParts, "  ")))
	}
	if snap.cost != 0 {
		pills = append(pills, PillStyle.Render(formatCost(snap.cost)))
	}

	ctxStr := formatContextPill(snap.contextCurrent, snap.contextLimit)
	if ctxStr != "" {
		pills = append(pills, renderContextPill(ctxStr, snap.contextCurrent, snap.contextLimit))
	}
	return pills
}

func renderStatusBarViewingPill(label, color string) string {
	if strings.TrimSpace(color) == "" {
		return PillStyle.Render("◉ " + label)
	}
	return PillStyle.Foreground(lipgloss.Color(color)).Render("◉ " + label)
}

func renderMCPStatusPill(rows []agent.MCPServerDisplay) string {
	if len(rows) == 0 {
		return ""
	}

	okCount := 0
	pendingCount := 0
	failCount := 0
	for _, row := range rows {
		switch {
		case row.OK:
			okCount++
		case row.Pending:
			pendingCount++
		default:
			failCount++
		}
	}

	color := "82"
	switch {
	case failCount > 0:
		color = "196"
	case pendingCount > 0:
		color = "240"
	}

	label := "mcp"
	if len(rows) == 1 {
		label = "mcp:" + rows[0].Name
	} else {
		label = fmt.Sprintf("mcp:%d/%d", okCount, len(rows))
	}
	return PillStyle.Foreground(lipgloss.Color(color)).Render(label)
}

func formatContextPill(current, limit int) string {
	if current == 0 {
		return ""
	}
	tok := formatTokens(current)
	if limit <= 0 {
		return "(" + tok + ")"
	}
	pct := int(float64(current) / float64(limit) * 100)
	return fmt.Sprintf("%d%% (%s)", pct, tok)
}

func renderContextPill(text string, current, limit int) string {
	if limit <= 0 {
		return PillStyle.Render(text)
	}
	pct := float64(current) / float64(limit)
	var fg string
	switch {
	case pct >= 0.85:
		fg = "196"
	case pct >= 0.60:
		fg = "220"
	default:
		fg = "82"
	}
	return PillStyle.Foreground(lipgloss.Color(fg)).Render(text)
}
