package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/lipgloss/v2"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

// infoPanelSectionAutoCollapsed reports the state a section takes with no
// manual override: state-driven sections follow live integration state, GIT
// stays folded because its header already carries the summary, and
// content-driven sections open as soon as their first entry renders.
func (m *Model) infoPanelSectionAutoCollapsed(section infoPanelSectionID) bool {
	switch section {
	case infoPanelSectionLSP, infoPanelSectionMCP, infoPanelSectionSkills:
		return m.captureInfoPanelIntegrations().collapsed(section)
	case infoPanelSectionGit:
		return true
	default:
		return false
	}
}

// User choices override the default derived from current integration state.
func (m *Model) infoPanelSectionCollapsed(section infoPanelSectionID, defaultCollapsed bool) bool {
	if collapsed, ok := m.infoPanelCollapsedSections[section]; ok {
		return collapsed
	}
	return defaultCollapsed
}

func (m *Model) isInfoPanelSectionCollapsed(section infoPanelSectionID) bool {
	return m.infoPanelSectionCollapsed(section, m.infoPanelSectionAutoCollapsed(section))
}

// toggleInfoPanelSection flips a section between folded and expanded. A manual
// choice sticks across content changes, but clicking the section back onto the
// state the automatic rules already chose releases the override, so the
// section follows its content again from the next change on.
func (m *Model) toggleInfoPanelSection(section infoPanelSectionID) {
	if m.infoPanelCollapsedSections == nil {
		m.infoPanelCollapsedSections = make(map[infoPanelSectionID]bool)
	}
	auto := m.infoPanelSectionAutoCollapsed(section)
	next := !m.infoPanelSectionCollapsed(section, auto)
	if next == auto {
		delete(m.infoPanelCollapsedSections, section)
	} else {
		m.infoPanelCollapsedSections[section] = next
	}
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
	return currentStyles.InfoPanelTitle.Render(fmt.Sprintf("%s %s", marker, title))
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
		return currentStyles.InfoPanelLineBg.Width(lineW).Render(left)
	}
	budget := infoPanelCollapsibleSummaryBudget(lineW, expanded, title)
	if budget <= 0 {
		// Too narrow for the separator: fall back to truncating the summary
		// against the width left after the title.
		availSummary := lineW - lipgloss.Width(left)
		if availSummary <= 0 {
			return currentStyles.InfoPanelLineBg.Width(lineW).Render(left)
		}
		return currentStyles.InfoPanelLineBg.Width(lineW).Render(lipgloss.JoinHorizontal(
			lipgloss.Left,
			left,
			currentStyles.InfoPanelDim.Render(terminaltext.TruncateOneLine(summary, availSummary)),
		))
	}
	right := lipgloss.JoinHorizontal(
		lipgloss.Left,
		currentStyles.InfoPanelDim.Render(infoPanelCollapsibleSummarySep),
		currentStyles.InfoPanelDim.Render(terminaltext.TruncateOneLine(summary, budget)),
	)
	line := lipgloss.JoinHorizontal(lipgloss.Left, left, right)
	return currentStyles.InfoPanelLineBg.Width(lineW).Render(line)
}
