package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/agent"
)

// defaultInfoPanelSectionCollapsed reports whether a section the user has not
// toggled starts folded.
//
// Folded is the default everywhere: a section only defaults to expanded once
// it holds content worth showing. That distinction matters most for the
// state-driven sections, which list every configured entry even while all of
// them are merely pending, disabled, idle, or unloaded — an LSP server still
// starting up, an MCP server that has not connected yet, a skill nobody
// loaded. Those stay folded until at least one entry reaches its active
// state, so a session opens quiet instead of expanding integrations that are
// not doing anything, and they unfold on their own the moment something
// loads.
//
// The remaining sections only render once the session has produced their
// content (a todo, an edit, a sub-agent, a running job), so that content is
// the trigger and they stay expanded by default. GIT stays folded because its
// header already carries the branch and sync summary.
func (m *Model) defaultInfoPanelSectionCollapsed(section infoPanelSectionID) bool {
	switch section {
	case infoPanelSectionLSP:
		return !m.infoPanelAnyLSPServerLoaded()
	case infoPanelSectionMCP:
		return !m.infoPanelAnyMCPServerConnected()
	case infoPanelSectionSkills:
		return !m.infoPanelAnySkillLoaded()
	default:
		return section == infoPanelSectionGit
	}
}

func (m *Model) isInfoPanelSectionCollapsed(section infoPanelSectionID) bool {
	if m.infoPanelCollapsedSections == nil {
		return m.defaultInfoPanelSectionCollapsed(section)
	}
	collapsed, ok := m.infoPanelCollapsedSections[section]
	if !ok {
		return m.defaultInfoPanelSectionCollapsed(section)
	}
	return collapsed
}

// infoPanelAnyLSPServerLoaded reports whether at least one language server has
// finished loading. Servers that are still starting, failed, or auto-unloaded
// while idle do not count: until a server actually serves diagnostics the LSP
// section defaults to folded.
func (m *Model) infoPanelAnyLSPServerLoaded() bool {
	lp, ok := m.agent.(agent.LSPStateProvider)
	if !ok {
		return false
	}
	for _, row := range lp.LSPServerList() {
		if row.OK {
			return true
		}
	}
	return false
}

// infoPanelAnyMCPServerConnected reports whether at least one MCP server is
// connected. Pending, retrying, disabled, idle, and failed servers do not
// count: until a server is live the MCP section defaults to folded.
func (m *Model) infoPanelAnyMCPServerConnected() bool {
	mp, ok := m.agent.(agent.MCPStateProvider)
	if !ok {
		return false
	}
	for _, row := range mp.MCPServerList() {
		if row.OK {
			return true
		}
	}
	return false
}

// infoPanelAnySkillLoaded reports whether at least one skill has been loaded
// into the focused agent's context. Available-but-unloaded skills do not
// count, so the SKILLS section stays folded until something is actually
// loaded.
//
// This reads the same invoked-skill map the section marks its loaded rows
// from. The history-derived load state those rows also apply can trail the
// map by a beat, but both sources are resynced from the messages that are
// actually in context, so the default cannot disagree with the rows for long.
func (m *Model) infoPanelAnySkillLoaded() bool {
	if m.agent == nil {
		return false
	}
	for _, sk := range m.agent.InvokedSkills() {
		if sk != nil && strings.TrimSpace(sk.Name) != "" {
			return true
		}
	}
	return false
}

func (m *Model) toggleInfoPanelSection(section infoPanelSectionID) {
	if m.infoPanelCollapsedSections == nil {
		m.infoPanelCollapsedSections = make(map[infoPanelSectionID]bool)
	}
	m.infoPanelCollapsedSections[section] = !m.isInfoPanelSectionCollapsed(section)
	m.clearInfoPanelRenderCache()
	m.infoPanelHitBoxes = nil
}

func (m *Model) infoPanelSectionAtPoint(x, y int) (infoPanelSectionID, bool) {
	if m.layout.infoPanel.Dx() <= 0 || m.layout.infoPanel.Dy() <= 0 {
		return "", false
	}
	if x < m.layout.infoPanel.Min.X || x >= m.layout.infoPanel.Max.X || y < m.layout.infoPanel.Min.Y || y >= m.layout.infoPanel.Max.Y {
		return "", false
	}
	localY := y - m.layout.infoPanel.Min.Y + m.infoPanelScrollOffset
	for _, hit := range m.infoPanelHitBoxes {
		if hit.section == "" {
			continue
		}
		if localY >= hit.startY && localY < hit.endY {
			return hit.section, true
		}
	}
	return "", false
}

func (m *Model) infoPanelAgentAtPoint(x, y int) (string, bool) {
	if m.layout.infoPanel.Dx() <= 0 || m.layout.infoPanel.Dy() <= 0 {
		return "", false
	}
	if x < m.layout.infoPanel.Min.X || x >= m.layout.infoPanel.Max.X || y < m.layout.infoPanel.Min.Y || y >= m.layout.infoPanel.Max.Y {
		return "", false
	}
	localY := y - m.layout.infoPanel.Min.Y + m.infoPanelScrollOffset
	for _, hit := range m.infoPanelHitBoxes {
		if hit.agentID == "" {
			continue
		}
		if localY >= hit.startY && localY < hit.endY {
			return hit.agentID, true
		}
	}
	return "", false
}

func (m *Model) beginInfoPanelRenderPass() {
	m.infoPanelHitBoxes = m.infoPanelHitBoxes[:0]
	m.infoPanelRenderCursorY = 0
}

func (m *Model) recordInfoPanelSectionHitBox(section infoPanelSectionID, rendered string) {
	if rendered == "" {
		return
	}
	height := lipgloss.Height(rendered)
	if section != "" {
		m.infoPanelHitBoxes = append(m.infoPanelHitBoxes, infoPanelSectionHitBox{
			section: section,
			startY:  m.infoPanelRenderCursorY,
			endY:    m.infoPanelRenderCursorY + 1,
		})
	}
	m.infoPanelRenderCursorY += height + 1
}

func (m *Model) recordInfoPanelAgentHitBox(agentID string, startY, endY int) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || endY <= startY {
		return
	}
	m.infoPanelHitBoxes = append(m.infoPanelHitBoxes, infoPanelSectionHitBox{
		agentID: agentID,
		startY:  startY,
		endY:    endY,
	})
}

const (
	infoPanelCollapsibleMarkerOpen   = "▼"
	infoPanelCollapsibleMarkerClosed = "▶"
	infoPanelCollapsibleSummarySep   = " · "
)

func renderInfoPanelCollapsibleHeaderLeft(expanded bool, title string) string {
	marker := infoPanelCollapsibleMarkerClosed
	if expanded {
		marker = infoPanelCollapsibleMarkerOpen
	}
	return InfoPanelTitle.Render(fmt.Sprintf("%s %s", marker, title))
}

// infoPanelCollapsibleSummaryBudget returns the max display width left for a
// section summary next to its header title and separator on one row, or 0 when
// the row is too narrow to hold the separator plus any summary.
func infoPanelCollapsibleSummaryBudget(lineW int, expanded bool, title string) int {
	availSummary := lineW - lipgloss.Width(renderInfoPanelCollapsibleHeaderLeft(expanded, title))
	sepWidth := lipgloss.Width(infoPanelCollapsibleSummarySep)
	if availSummary <= sepWidth {
		return 0
	}
	return availSummary - sepWidth
}

func renderInfoPanelCollapsibleHeader(lineW int, expanded bool, title string, summary string) string {
	left := renderInfoPanelCollapsibleHeaderLeft(expanded, title)
	if summary == "" {
		return InfoPanelLineBg.Width(lineW).Render(left)
	}
	budget := infoPanelCollapsibleSummaryBudget(lineW, expanded, title)
	if budget <= 0 {
		// Too narrow for the separator: fall back to truncating the summary
		// against the width left after the title.
		availSummary := lineW - lipgloss.Width(left)
		if availSummary <= 0 {
			return InfoPanelLineBg.Width(lineW).Render(left)
		}
		return InfoPanelLineBg.Width(lineW).Render(lipgloss.JoinHorizontal(
			lipgloss.Left,
			left,
			InfoPanelDim.Render(truncateOneLine(summary, availSummary)),
		))
	}
	right := lipgloss.JoinHorizontal(
		lipgloss.Left,
		InfoPanelDim.Render(infoPanelCollapsibleSummarySep),
		InfoPanelDim.Render(truncateOneLine(summary, budget)),
	)
	line := lipgloss.JoinHorizontal(lipgloss.Left, left, right)
	return InfoPanelLineBg.Width(lineW).Render(line)
}
