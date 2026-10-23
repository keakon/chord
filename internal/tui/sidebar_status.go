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
func sidebarEntryText(entry SidebarEntry, focused bool, width int) (line, reason string) {
	if width <= 0 {
		return "", ""
	}
	indicator := sidebarIndicator(entry.Status, focused)
	iw := lipgloss.Width(indicator)
	if width <= iw {
		return terminaltext.TruncateOneLine(indicator, width), ""
	}
	nameWidth := width - iw - 1
	line = indicator
	if nameWidth > 0 {
		line += " " + sidebarRenderedName(entry, nameWidth)
	}
	if entry.ID == "main" || entry.Status != string(agent.SubAgentStateWaitingMain) {
		return line, ""
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
	return line, reason
}
