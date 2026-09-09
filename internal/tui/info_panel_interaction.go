package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

func defaultInfoPanelSectionCollapsed(section infoPanelSectionID) bool {
	return section == infoPanelSectionGit
}

func (m *Model) isInfoPanelSectionCollapsed(section infoPanelSectionID) bool {
	if m.infoPanelCollapsedSections == nil {
		return defaultInfoPanelSectionCollapsed(section)
	}
	collapsed, ok := m.infoPanelCollapsedSections[section]
	if !ok {
		return defaultInfoPanelSectionCollapsed(section)
	}
	return collapsed
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
