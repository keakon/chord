package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// AgentsSummary counts only child tasks by their status icon, keeping
// attention-requiring states first.
func (s Sidebar) AgentsSummary() string {
	counts := make(map[string]int)
	for _, entry := range s.agents {
		if entry.ID != "main" {
			counts[statusIndicator(entry.Status)]++
		}
	}
	var parts []string
	for _, icon := range []string{
		statusIndicator(string(agent.SubAgentStateWaitingMain)),
		statusIndicator(string(agent.SubAgentStateFailed)),
		statusIndicator(string(agent.SubAgentStateWaitingDescendant)),
		statusIndicator(string(agent.SubAgentStateRunning)),
		statusIndicator(string(agent.SubAgentStateIdle)),
		statusIndicator(string(agent.SubAgentStateCompleted)),
		statusIndicator(string(agent.SubAgentStateCancelled)),
		statusIndicator(""),
	} {
		if n := counts[icon]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, icon))
		}
	}
	if len(parts) == 0 {
		return "0 agents"
	}
	return strings.Join(parts, " · ")
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
