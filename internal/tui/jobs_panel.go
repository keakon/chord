package tui

import (
	"fmt"
	"strings"
	"time"
)

// jobRowHitBox marks one row of the JOBS section: the job it shows and, when
// the row draws a stop affordance, the panel-local columns that trigger it.
type jobRowHitBox struct {
	jobID          string
	startLine      int
	endLine        int
	stopZoneStartX int
	stopZoneEndX   int
}

// buildInfoPanelJobsBlock renders the JOBS section — one row per running or
// stopping job, in registry order — together with each row's hit box. It
// returns an empty block when no job is still running, hiding the section
// entirely rather than showing an empty header.
func (m *Model) buildInfoPanelJobsBlock(lineW int) (string, []jobRowHitBox) {
	jobs := m.activeJobs()
	if len(jobs) == 0 {
		return "", nil
	}
	expanded := !m.isInfoPanelSectionCollapsed(infoPanelSectionJobs)
	lines := []string{renderInfoPanelCollapsibleHeader(lineW, expanded, "JOBS", fmt.Sprintf("%d", len(jobs)))}
	if !expanded {
		// Collapsed: the header alone, and no elapsed values to keep refreshing.
		return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines)), nil
	}
	contentWidth := max(lineW-infoPanelCollapsibleContentInset, 0)
	styles := infoPanelJobRowStyles()
	now := time.Now()
	hits := make([]jobRowHitBox, 0, len(jobs))
	for _, job := range jobs {
		lineIndex := len(lines)
		row := renderJobRow(contentWidth, job, now, styles)
		lines = append(lines, renderInfoPanelCollapsibleContentLine(lineW, row.text))
		hit := jobRowHitBox{jobID: job.ID, startLine: lineIndex, endLine: lineIndex + 1}
		if row.stoppable {
			offset := infoPanelContentHorizontalPadding + infoPanelCollapsibleContentInset
			hit.stopZoneStartX = offset + row.stopZoneStart
			hit.stopZoneEndX = offset + row.stopZoneEnd
		}
		hits = append(hits, hit)
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines)), hits
}

func (m *Model) recordInfoPanelJobHitBox(jobID string, startY, endY, stopZoneStartX, stopZoneEndX int) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" || endY <= startY {
		return
	}
	m.infoPanelHitBoxes = append(m.infoPanelHitBoxes, infoPanelSectionHitBox{
		jobID:          jobID,
		startY:         startY,
		endY:           endY,
		stopZoneStartX: stopZoneStartX,
		stopZoneEndX:   stopZoneEndX,
	})
}

// infoPanelJobAtPoint resolves a panel click to a job row. inStopZone reports
// whether the click landed on the row's stop affordance; clicks on the rest of
// the row select nothing (the panel has no keyboard cursor for jobs).
func (m *Model) infoPanelJobAtPoint(x, y int) (jobID string, inStopZone bool, ok bool) {
	if m.layout.infoPanel.Dx() <= 0 || m.layout.infoPanel.Dy() <= 0 {
		return "", false, false
	}
	if x < m.layout.infoPanel.Min.X || x >= m.layout.infoPanel.Max.X || y < m.layout.infoPanel.Min.Y || y >= m.layout.infoPanel.Max.Y {
		return "", false, false
	}
	localX := x - m.layout.infoPanel.Min.X
	localY := y - m.layout.infoPanel.Min.Y + m.infoPanelScrollOffset
	for _, hit := range m.infoPanelHitBoxes {
		if hit.jobID == "" {
			continue
		}
		if localY >= hit.startY && localY < hit.endY {
			inStopZone := hit.stopZoneEndX > hit.stopZoneStartX &&
				localX >= hit.stopZoneStartX && localX < hit.stopZoneEndX
			return hit.jobID, inStopZone, true
		}
	}
	return "", false, false
}
