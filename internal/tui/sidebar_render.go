package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

// Visible returns true if the sidebar should be displayed (at least one
// SubAgent exists beyond the main agent).
func (s *Sidebar) Visible() bool {
	return len(s.agents) > 1
}

// Width returns the sidebar's column width (including border).
func (s *Sidebar) Width() int {
	return s.width
}

// AgentIDs returns the agent IDs used by Shift+Tab cycling. Main is always
// first, even before the sidebar has been refreshed.
func (s *Sidebar) AgentIDs() []string {
	ids := make([]string, 0, 1+len(s.agents))
	ids = append(ids, "main")
	for _, entry := range s.agents {
		if entry.ID == "main" {
			continue
		}
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
	summary := terminaltext.TruncateOneLine(s.AgentsSummary(), innerWidth)
	return currentStyles.InfoPanelAgentStatusStyle.Render(summary)
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

func sidebarTreePrefix(entry SidebarEntry) string {
	if entry.ID == "main" || entry.TreeDepth <= 0 {
		return ""
	}
	return strings.Repeat("  ", entry.TreeDepth-1) + "└ "
}

func sidebarRenderedName(entry SidebarEntry, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	prefix := sidebarTreePrefix(entry)
	prefixWidth := lipgloss.Width(prefix)
	if prefixWidth >= maxWidth {
		prefix = "└ "
		prefixWidth = lipgloss.Width(prefix)
		if prefixWidth >= maxWidth {
			return "└"
		}
	}
	return prefix + terminaltext.TruncateOneLine(sidebarEntryDisplayName(entry), maxWidth-prefixWidth)
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
		style := currentStyles.SidebarFocusedStyle
		if color != "" {
			style = style.Foreground(lipgloss.Color(color))
		}
		return style
	}
	style := currentStyles.SidebarEntryStyle
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
		style := currentStyles.InfoPanelAgentFocusedStyle
		if color != "" {
			style = style.Foreground(lipgloss.Color(color))
		}
		return style
	}
	style := currentStyles.InfoPanelAgentEntryStyle
	if color != "" {
		style = style.Foreground(lipgloss.Color(color))
	}
	return style
}

type sidebarRenderedLine struct {
	Text    string
	AgentID string
}

// buildInfoPanelRenderedLines is like buildLines but uses InfoPanelLineBg-compatible
// styling and keeps row ownership so the AGENTS block can map mouse clicks back to agent IDs.
func (s Sidebar) buildInfoPanelRenderedLines(innerWidth int) []sidebarRenderedLine {
	var lines []sidebarRenderedLine
	for _, entry := range s.agents {
		isFocused := s.isFocused(entry.ID)
		line, reason := sidebarEntryText(entry, isFocused, innerWidth)

		if isFocused {
			line = infoPanelAgentRowStyle(entry, true).Width(innerWidth).Render(line)
		} else if entry.ID == "main" {
			line = infoPanelAgentRowStyle(entry, false).Width(innerWidth).Render(line)
		} else {
			line = currentStyles.InfoPanelLineBg.Width(innerWidth).Render(infoPanelAgentRowStyle(entry, false).Render(line))
		}
		lines = append(lines, sidebarRenderedLine{Text: line, AgentID: entry.ID})
		if reason != "" {
			style := currentStyles.InfoPanelDim
			if isFocused {
				style = currentStyles.InfoPanelAgentFocusedStyle.Foreground(currentStyles.InfoPanelDim.GetForeground())
			}
			lines = append(lines, sidebarRenderedLine{Text: style.Width(innerWidth).Render(reason), AgentID: entry.ID})
		}
	}
	return lines
}

// buildLines constructs the per-entry display lines for the agent list.
// Each agent is rendered as a compact single line: "{indicator} {short-name}".
// Up to 3 recently changed files are shown as sub-lines beneath each agent.
func (s Sidebar) buildLines(innerWidth int) []string {
	var lines []string
	for _, entry := range s.agents {
		isFocused := s.isFocused(entry.ID)
		line, reason := sidebarEntryText(entry, isFocused, innerWidth)

		if isFocused {
			line = sidebarEntryStyle(entry, true).Width(innerWidth).Render(line)
		} else {
			line = sidebarEntryStyle(entry, false).Width(innerWidth).Render(line)
		}
		lines = append(lines, line)
		if reason != "" {
			lines = append(lines, sidebarEntryStyle(entry, isFocused).Width(innerWidth).Render(reason))
		}

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
			if !fe.Deleted {
				if fe.Added > 0 {
					parts += currentStyles.SidebarAddedStyle.Render(fmt.Sprintf("+%d", fe.Added))
				}
				if fe.Removed > 0 {
					if parts != "" {
						parts += " "
					}
					parts += currentStyles.SidebarRemovedStyle.Render(fmt.Sprintf("-%d", fe.Removed))
				}
			}
			statStr := parts
			statW := 0
			if statStr != "" {
				statW = lipgloss.Width(statStr)
			}
			// Reserve the 2-space indent, plus the separator and stats width only
			// when stats are present. Prioritize complete stats over long filenames.
			nameW := innerWidth - 2
			if statStr != "" {
				nameW -= statW + 1
			}
			if nameW < 0 {
				nameW = 0
			}
			baseName = terminaltext.TruncateOneLine(baseName, nameW)
			namePart := ""
			if baseName != "" {
				namePart = currentStyles.SidebarFileStyle.Render(baseName)
			}
			if fe.Deleted {
				namePart = currentStyles.SidebarFileStyle.Strikethrough(true).Render(baseName)
			}
			sep := ""
			if namePart != "" && statStr != "" {
				sep = currentStyles.SidebarFileStyle.Render(" ")
			}
			fileLine := currentStyles.SidebarFileStyle.Render("  ") + namePart + sep + statStr
			lines = append(lines, fileLine)
		}
		if extra > 0 {
			lines = append(lines, currentStyles.SidebarFileStyle.Render(fmt.Sprintf("  +%d more", extra)))
		}
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
//
// The reachable inputs are the sub-agent states and the "done"/"error" names
// AgentStatusEvent carries for them.
func sidebarIndicator(status string, focused bool) string {
	prefix := " "
	if focused {
		prefix = "▸"
	}
	return prefix + " " + statusIndicator(status)
}

func statusIndicator(status string) string {
	switch status {
	case "running":
		return "○"
	case "waiting_main", "waiting_descendant":
		return "?"
	case "done", "completed":
		return "✓"
	case "cancelled":
		return "⊘"
	case "error", "failed":
		return "✗"
	case "idle":
		return "…"
	default:
		return "·"
	}
}
