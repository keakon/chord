package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
)

func sidebarStatusLabel(status string) string {
	switch status {
	case string(agent.SubAgentStateRunning):
		return "running"
	case string(agent.SubAgentStateWaitingMain):
		return "awaiting owner"
	case string(agent.SubAgentStateWaitingDescendant):
		return "awaiting child"
	case string(agent.SubAgentStateCompleted), subAgentStatusDone:
		return "delivered"
	case string(agent.SubAgentStateFailed), subAgentStatusError:
		return "failed"
	case string(agent.SubAgentStateCancelled):
		return "cancelled"
	case string(agent.SubAgentStateIdle):
		return "idle"
	default:
		return "unknown"
	}
}

// AgentsSummary counts only child tasks, keeping attention-requiring states first.
func (s Sidebar) AgentsSummary() string {
	counts := make(map[string]int)
	for _, entry := range s.agents {
		if entry.ID != "main" {
			counts[sidebarStatusLabel(entry.Status)]++
		}
	}
	var parts []string
	for _, label := range []string{"awaiting owner", "failed", "awaiting child", "running", "idle", "delivered", "cancelled", "unknown"} {
		if n := counts[label]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
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
		return truncateOneLine(indicator, width), ""
	}
	nameWidth := width - iw - 1
	label := ""
	if entry.ID != "main" {
		status := sidebarStatusLabel(entry.Status)
		if nameWidth >= lipgloss.Width(sidebarTreePrefix(entry))+4+2+lipgloss.Width(status) {
			label = "  " + status
			nameWidth -= lipgloss.Width(label)
		}
	}
	line = indicator
	if nameWidth > 0 {
		line += " " + sidebarRenderedName(entry, nameWidth) + label
	}
	if entry.ID == "main" || entry.Status != string(agent.SubAgentStateWaitingMain) {
		return line, ""
	}
	text := strings.Join(strings.Fields(sanitizeDisplayText(entry.AwaitingOwnerReason)), " ")
	prefixWidth := lipgloss.Width(sidebarTreePrefix(entry))
	if prefixWidth >= nameWidth {
		prefixWidth = min(2, nameWidth)
	}
	indent := iw + 1 + prefixWidth
	if text != "" && width > indent {
		reason = strings.Repeat(" ", indent) + truncateOneLine(text, width-indent)
	}
	return line, reason
}
