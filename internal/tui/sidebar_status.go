package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// AgentsSummary reports the child count for the section header. Each agent row
// already carries its own status icon, so the header stays a plain count for
// consistency with the LSP and MCP headers.
func (s Sidebar) AgentsSummary() string {
	count := 0
	for _, entry := range s.agents {
		if entry.ID != "main" {
			count++
		}
	}
	if count == 0 {
		return "0 agents"
	}
	return fmt.Sprint(count)
}

// sidebarEntryText computes plain text only; containers supply their own styles.
// The usage line renders as an indented dim sub-line like the reason line.
func sidebarEntryText(entry SidebarEntry, focused bool, width int) (line, reason, usage string) {
	if width <= 0 {
		return "", "", ""
	}
	indicator := sidebarIndicator(entry.Status, focused)
	iw := lipgloss.Width(indicator)
	if width <= iw {
		return terminaltext.TruncateOneLine(indicator, width), "", ""
	}
	nameWidth := width - iw - 1
	line = indicator
	if nameWidth > 0 {
		line += " " + sidebarRenderedName(entry, nameWidth)
	}
	usage = sidebarUsageText(entry, width, iw)
	if entry.ID == "main" || entry.Status != string(agent.SubAgentStateWaitingMain) {
		return line, "", usage
	}
	text := strings.Join(strings.Fields(terminaltext.Sanitize(entry.AwaitingOwnerReason)), " ")
	prefixWidth := lipgloss.Width(sidebarTreePrefix(entry))
	if prefixWidth >= nameWidth {
		prefixWidth = min(2, nameWidth)
	}
	indent := iw + 1 + prefixWidth
	if text != "" && width > indent {
		reason = strings.Repeat(" ", indent) + terminaltext.TruncateOneLine(text, width-indent)
	}
	return line, reason, usage
}

// sidebarUsageText renders one entry's cumulative usage as "48.2k tok · $0.42",
// indented under the name. Zero usage renders nothing.
func sidebarUsageText(entry SidebarEntry, width, indicatorWidth int) string {
	if entry.Usage.Zero() {
		return ""
	}
	text := formatTokens(entry.Usage.TotalTokens()) + " tok"
	if entry.Usage.CostKnown {
		text += " · $" + formatSidebarCost(entry.Usage.CostUSD)
	}
	indent := indicatorWidth + 1
	if width <= indent {
		return ""
	}
	return strings.Repeat(" ", indent) + terminaltext.TruncateOneLine(text, width-indent)
}

// formatSidebarCost keeps small per-task costs readable: two decimals once the
// value reaches a cent, otherwise enough digits to stay non-zero. A zero cost
// is a real report (subscription logins price usage at zero), not a missing one.
func formatSidebarCost(v float64) string {
	switch {
	case v == 0:
		return "0"
	case v >= 0.01:
		return fmt.Sprintf("%.2f", v)
	default:
		return fmt.Sprintf("%.4f", v)
	}
}
