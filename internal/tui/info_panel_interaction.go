package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

func (m *Model) isInfoPanelSectionCollapsed(section infoPanelSectionID) bool {
	if m.infoPanelCollapsedSections == nil {
		return false
	}
	return m.infoPanelCollapsedSections[section]
}

func (m *Model) toggleInfoPanelSection(section infoPanelSectionID) {
	if m.infoPanelCollapsedSections == nil {
		m.infoPanelCollapsedSections = make(map[infoPanelSectionID]bool)
	}
	m.infoPanelCollapsedSections[section] = !m.infoPanelCollapsedSections[section]
	m.cachedInfoPanelW = 0
	m.cachedInfoPanelH = 0
	m.cachedInfoPanelFP = ""
	m.cachedInfoPanelOut = ""
	m.infoPanelHitBoxes = nil
}

func (m *Model) infoPanelSectionAtPoint(x, y int) (infoPanelSectionID, bool) {
	if m.layout.infoPanel.Dx() <= 0 || m.layout.infoPanel.Dy() <= 0 {
		return "", false
	}
	if x < m.layout.infoPanel.Min.X || x >= m.layout.infoPanel.Max.X || y < m.layout.infoPanel.Min.Y || y >= m.layout.infoPanel.Max.Y {
		return "", false
	}
	localY := y - m.layout.infoPanel.Min.Y
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
	localY := y - m.layout.infoPanel.Min.Y
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

func renderInfoPanelCollapsibleHeader(lineW int, expanded bool, title string, summary string) string {
	marker := "▶"
	if expanded {
		marker = "▼"
	}
	left := InfoPanelTitle.Render(fmt.Sprintf("%s %s", marker, title))
	if summary == "" {
		return InfoPanelLineBg.Width(lineW).Render(left)
	}
	availSummary := lineW - lipgloss.Width(left)
	if availSummary <= 0 {
		return InfoPanelLineBg.Width(lineW).Render(left)
	}
	sepText := " · "
	sepWidth := lipgloss.Width(sepText)
	var right string
	if availSummary <= sepWidth {
		right = InfoPanelDim.Render(truncateOneLine(summary, availSummary))
	} else {
		right = lipgloss.JoinHorizontal(
			lipgloss.Left,
			InfoPanelDim.Render(sepText),
			InfoPanelDim.Render(truncateOneLine(summary, availSummary-sepWidth)),
		)
	}
	line := lipgloss.JoinHorizontal(lipgloss.Left, left, right)
	return InfoPanelLineBg.Width(lineW).Render(line)
}
