package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
)

// Visible returns true if the sidebar should be displayed (at least one
// SubAgent or pending placeholder exists beyond the main agent).
func (s *Sidebar) Visible() bool {
	return len(s.agents) > 1 || s.pendingTasks > 0
}

// Width returns the sidebar's column width (including border).
func (s *Sidebar) Width() int {
	return s.width
}

// AgentIDs returns the ordered list of agent IDs shown in the sidebar.
// Used by Tab-key cycling logic. Returns nil if only main agent.
func (s *Sidebar) AgentIDs() []string {
	if len(s.agents) <= 1 {
		return nil
	}
	ids := make([]string, 0, len(s.agents))
	for _, entry := range s.agents {
		ids = append(ids, entry.ID)
	}
	return ids
}

// ViewCompact renders the agent list as a compact block without fixed-height
// padding. Intended for embedding at the top of the right info panel.
func (s Sidebar) ViewCompact(innerWidth int) string {
	if !s.Visible() || innerWidth <= 0 {
		return ""
	}
	lines := s.buildLines(innerWidth)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

// ViewLines returns the agent list as individual lines (no border, no padding).
// Intended for embedding line-by-line into the right info panel.
func (s Sidebar) ViewLines(innerWidth int) []string {
	if !s.Visible() || innerWidth <= 0 {
		return nil
	}
	return s.buildLines(innerWidth)
}

// ViewInfoPanelLines returns agent lines styled for the info panel.
// Unlike buildLines, every fragment derives from InfoPanelBg-compatible styles so
// per-line background rendering stays continuous inside the right panel.
func (s Sidebar) ViewInfoPanelLines(innerWidth int) []string {
	if !s.Visible() || innerWidth <= 0 {
		return nil
	}
	rows := s.buildInfoPanelRenderedLines(innerWidth)
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, row.Text)
	}
	return lines
}

func (s Sidebar) ViewInfoPanelSummaryLine(innerWidth int) string {
	if !s.Visible() || innerWidth <= 0 {
		return ""
	}
	summary := truncateOneLine(s.AgentsSummary(), innerWidth)
	return InfoPanelAgentStatusStyle.Render(summary)
}

func sidebarEntryDisplayName(entry SidebarEntry) string {
	if entry.ID == "main" {
		if strings.TrimSpace(entry.TaskDesc) != "" {
			return strings.TrimSpace(entry.TaskDesc)
		}
		if strings.TrimSpace(entry.AgentDefName) != "" {
			return strings.TrimSpace(entry.AgentDefName)
		}
		return "main"
	}
	if strings.TrimSpace(entry.TaskDesc) != "" {
		return strings.TrimSpace(entry.TaskDesc)
	}
	if strings.TrimSpace(entry.AgentDefName) != "" {
		return strings.TrimSpace(entry.AgentDefName)
	}
	return entry.ID
}

func sidebarEntryStyle(entry SidebarEntry, focused bool) lipgloss.Style {
	color := strings.TrimSpace(entry.Color)
	if color == "" {
		switch entry.AgentDefName {
		case "orchestrator":
			color = "45"
		case "expert":
			color = "214"
		case "coder":
			color = "81"
		case "reviewer":
			color = "70"
		case "explorer":
			color = "141"
		}
	}
	if focused {
		style := SidebarFocusedStyle
		if color != "" {
			style = style.Foreground(lipgloss.Color(color))
		}
		return style
	}
	style := SidebarEntryStyle
	if color != "" {
		style = style.Foreground(lipgloss.Color(color))
	}
	return style
}

func infoPanelAgentRowStyle(entry SidebarEntry, focused bool) lipgloss.Style {
	color := strings.TrimSpace(entry.Color)
	if color == "" {
		switch entry.AgentDefName {
		case "orchestrator":
			color = "45"
		case "expert":
			color = "214"
		case "coder":
			color = "81"
		case "reviewer":
			color = "70"
		case "explorer":
			color = "141"
		}
	}
	if focused {
		style := InfoPanelAgentFocusedStyle
		if color != "" {
			style = style.Foreground(lipgloss.Color(color))
		}
		return style
	}
	style := InfoPanelAgentEntryStyle
	if color != "" {
		style = style.Foreground(lipgloss.Color(color))
	}
	return style
}

type sidebarRenderedLine struct {
	Text    string
	AgentID string
}

func renderSidebarPendingPlaceholder(width int) string {
	return SidebarEntryStyle.Width(width).Render(
		SidebarStatusStyle.Render(statusIndicator("pending", false)),
	)
}

func renderInfoPanelPendingPlaceholder(width int) string {
	return InfoPanelLineBg.Width(width).Render(
		InfoPanelAgentStatusStyle.Render(statusIndicator("pending", false)),
	)
}

// buildInfoPanelRenderedLines is like buildLines but uses InfoPanelLineBg-compatible
// styling and keeps row ownership so the AGENTS block can map mouse clicks back to agent IDs.
func (s Sidebar) buildInfoPanelRenderedLines(innerWidth int) []sidebarRenderedLine {
	var lines []sidebarRenderedLine
	for _, entry := range s.agents {
		isFocused := s.isFocused(entry.ID)
		indicator := statusIndicator(entry.Status, isFocused)

		name := sidebarEntryDisplayName(entry)

		var line string
		if isFocused || entry.ID == "main" {
			name = truncateOneLine(name, innerWidth-2)
			line = fmt.Sprintf("%s %s", indicator, name)
		} else {
			name = truncateOneLine(name, max(4, innerWidth-2))
			line = InfoPanelAgentEntryStyle.Render(fmt.Sprintf("%s %s", indicator, name))
		}

		if isFocused {
			line = infoPanelAgentRowStyle(entry, true).Width(innerWidth).Render(line)
		} else if entry.ID == "main" {
			line = infoPanelAgentRowStyle(entry, false).Width(innerWidth).Render(line)
		} else {
			line = InfoPanelLineBg.Width(innerWidth).Render(infoPanelAgentRowStyle(entry, false).Render(fmt.Sprintf("%s %s", indicator, name)))
		}
		lines = append(lines, sidebarRenderedLine{Text: line, AgentID: entry.ID})
	}
	for range s.pendingTasks {
		lines = append(lines, sidebarRenderedLine{Text: renderInfoPanelPendingPlaceholder(innerWidth)})
	}
	return lines
}

// buildLines constructs the per-entry display lines for the agent list.
// Each agent is rendered as a compact single line: "{indicator} {short-name}".
// Up to 3 recently edited files are shown as sub-lines beneath each agent.
func (s Sidebar) buildLines(innerWidth int) []string {
	var lines []string
	for _, entry := range s.agents {
		isFocused := s.isFocused(entry.ID)
		indicator := statusIndicator(entry.Status, isFocused)

		name := sidebarEntryDisplayName(entry)

		var line string
		if isFocused || entry.ID == "main" {
			name = truncateOneLine(name, innerWidth-2)
			line = fmt.Sprintf("%s %s", indicator, name)
		} else {
			name = truncateOneLine(name, max(4, innerWidth-2))
			line = fmt.Sprintf("%s %s", indicator, name)
		}

		if isFocused {
			line = sidebarEntryStyle(entry, true).Width(innerWidth).Render(line)
		} else {
			line = sidebarEntryStyle(entry, false).Width(innerWidth).Render(line)
		}
		lines = append(lines, line)

		files := entry.EditedFiles
		const maxFiles = 3
		extra := 0
		if len(files) > maxFiles {
			extra = len(files) - maxFiles
			files = files[len(files)-maxFiles:]
		}
		for _, fe := range files {
			baseName := filepath.Base(fe.Path)
			var parts string
			if fe.Added > 0 {
				parts += SidebarAddedStyle.Render(fmt.Sprintf("+%d", fe.Added))
			}
			if fe.Removed > 0 {
				if parts != "" {
					parts += " "
				}
				parts += SidebarRemovedStyle.Render(fmt.Sprintf("-%d", fe.Removed))
			}
			statStr := parts
			maxStatW := len(fmt.Sprintf("+%d -%d", fe.Added, fe.Removed))
			maxNameW := innerWidth - 2 - 1 - maxStatW
			if maxNameW < 4 {
				maxNameW = 4
			}
			baseName = truncateOneLine(baseName, maxNameW)
			fileLine := SidebarFileStyle.Render(fmt.Sprintf("  %s ", baseName)) + statStr
			lines = append(lines, fileLine)
		}
		if extra > 0 {
			lines = append(lines, SidebarFileStyle.Render(fmt.Sprintf("  +%d more", extra)))
		}
	}
	for range s.pendingTasks {
		lines = append(lines, renderSidebarPendingPlaceholder(innerWidth))
	}
	return lines
}

// isFocused returns true if the given agent ID is the currently focused agent.
func (s *Sidebar) isFocused(agentID string) bool {
	focused := s.focusedID
	if focused == "" {
		focused = "main"
	}
	return agentID == focused
}

// statusIndicator returns the Unicode status character for an agent.
func statusIndicator(status string, focused bool) string {
	if focused {
		return "●"
	}
	switch status {
	case "streaming", "executing", "running":
		return "○"
	case "connecting", "waiting_headers", "waiting_token", "retrying", "retrying_key", "cooling":
		return "↺"
	case "waiting_primary", "waiting_descendant":
		return "?"
	case "done", "completed":
		return "✓"
	case "cancelled":
		return "⊘"
	case "error":
		return "✗"
	case "idle":
		return "…"
	case "pending":
		return "◌"
	default:
		return "○"
	}
}
