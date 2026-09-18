package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// Background job lifecycle names as tools.SnapshotJobs reports them. Only these
// two mean the job is still running; completed/killed/failed are terminal.
const (
	jobStatusRunning  = "running"
	jobStatusStopping = "stopping"
)

const (
	// jobElapsedColumnWidth caps how much of a row tools.FormatElapsed may take:
	// the value is rendered at its natural width (a fixed nine-cell column would
	// spend seven cells on "8s" and truncate the label for nothing), and only a
	// pathological value ("1000h02m03s" and beyond) is clamped so it cannot push
	// the stop affordance out of its hit zone.
	jobElapsedColumnWidth = len("1h02m03s")
	// jobStopZoneCells is the clickable width at a row's right end that opens
	// the stop confirmation for a running job.
	jobStopZoneCells = 4
	// jobStopGlyph is the row's stop affordance.
	jobStopGlyph = "x"
	// infoPanelContentHorizontalPadding is InfoPanelStyle's left padding, i.e.
	// the panel-local column where lineW-wide content starts.
	infoPanelContentHorizontalPadding = 1
)

// agentEventMayChangeJobState reports whether an event can create, finish, or
// stop a background job, so the shared job snapshot and its animation refresh
// only when the answer can differ instead of on every agent event.
func agentEventMayChangeJobState(msg agentEventMsg) bool {
	switch msg.event.(type) {
	case agent.ToolResultEvent, agent.BackgroundResultAppendedEvent:
		return true
	default:
		return false
	}
}

// activeJobs returns this frame's running/stopping jobs, in registry order.
// The registry lock makes tools.SnapshotJobs unsuitable for per-widget polling,
// so the result is cached and shared: one call per rendered frame (identified by
// renderFrameGeneration) covers the info panel, the status bar, and the title.
func (m *Model) activeJobs() []tools.JobState {
	m.refreshJobSnapshotIfStale()
	return m.activeJobsSnapshot
}

func (m *Model) refreshJobSnapshotIfStale() {
	if m.jobsSnapshotValid && m.jobsSnapshotFrame == m.renderFrameGeneration {
		return
	}
	m.jobsSnapshot = tools.SnapshotJobs()
	m.activeJobsSnapshot = m.activeJobsSnapshot[:0]
	for _, state := range m.jobsSnapshot {
		switch state.Status {
		case jobStatusRunning, jobStatusStopping:
			m.activeJobsSnapshot = append(m.activeJobsSnapshot, state)
		}
	}
	m.jobsSnapshotValid = true
	m.jobsSnapshotFrame = m.renderFrameGeneration
}

func (m *Model) invalidateJobSnapshot() {
	m.jobsSnapshotValid = false
	// The jobs overlay and the stop dialog cache their rendered text; a job
	// state change must not leave them showing rows for jobs that are gone or
	// missing jobs that just appeared.
	m.jobsOverlay.renderCacheText = ""
	m.stopJobConfirm.renderCacheText = ""
}

// syncJobAnimation aligns the visual animation loop with background job
// activity: the first running job starts the loop and the last one finishing
// tears it down. The loop itself ends at the next animTick that finds no active
// animation, so no extra ticker is created here.
func (m *Model) syncJobAnimation() tea.Cmd {
	m.invalidateJobSnapshot()
	if len(m.activeJobs()) > 0 {
		return m.startActiveAnimation()
	}
	m.stopActiveAnimationIfIdle()
	return nil
}

// sidebarAgentStillRunning reports whether a sidebar status means the worker has
// not settled yet, using the same ordering that sinks idle and terminal states in
// the AGENTS section.
func sidebarAgentStillRunning(status string) bool {
	return sidebarStatusPriority(status) < sidebarStatusPriority("idle")
}

// activeSidebarWorkerCount counts the sidebar's non-main agents that are still
// working, backing the narrow-layout fallback pill.
func (m *Model) activeSidebarWorkerCount() int {
	count := 0
	for _, entry := range m.sidebar.Agents() {
		if entry.ID == "main" || !sidebarAgentStillRunning(entry.Status) {
			continue
		}
		count++
	}
	return count
}

// formatJobsActivityPill renders the narrow-layout fallback indicator. Jobs are
// kept in preference to agents: the agents-only form is never used while a job
// exists, and an empty result means the pill should be hidden (availWidth <= 0
// counts as no room).
func formatJobsActivityPill(runningJobs, agents, availWidth int) string {
	candidates := make([]string, 0, 2)
	switch {
	case runningJobs > 0:
		if agents > 0 {
			candidates = append(candidates,
				fmt.Sprintf("%s · %s", countedNoun(agents, "agent"), countedNoun(runningJobs, "job")))
		}
		candidates = append(candidates, countedNoun(runningJobs, "job"))
	case agents > 0:
		candidates = append(candidates, countedNoun(agents, "agent"))
	}
	for _, candidate := range candidates {
		if ansi.StringWidth(candidate) > availWidth {
			continue
		}
		return candidate
	}
	return ""
}

// countedNoun renders "<n> <noun>", pluralized with the shared tool-count
// helper so a noun that does not take a bare "s" (match → matches) cannot
// drift into a second, inconsistent rule here.
func countedNoun(count int, noun string) string {
	return fmt.Sprintf("%d %s", count, pluralizeToolCount(noun, count))
}

// jobRowStyles carries the panel- or dialog-hosted styles a job row is drawn
// with, so the info panel and the jobs overlay share one row renderer without
// either leaking its surface colors into the other.
type jobRowStyles struct {
	dotStyle     lipgloss.Style
	labelStyle   lipgloss.Style
	elapsedStyle lipgloss.Style
	stopStyle    lipgloss.Style
	gapStyle     lipgloss.Style
}

func infoPanelJobRowStyles() jobRowStyles {
	return jobRowStyles{
		dotStyle:     InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg)),
		labelStyle:   InfoPanelValue,
		elapsedStyle: InfoPanelDim,
		stopStyle:    InfoPanelDim,
		gapStyle:     InfoPanelDim,
	}
}

func dialogJobRowStyles() jobRowStyles {
	base := lipgloss.NewStyle().Background(lipgloss.Color(currentTheme.DialogBg))
	dim := base.Foreground(lipgloss.Color(currentTheme.DimFg))
	return jobRowStyles{
		dotStyle:     base.Foreground(lipgloss.Color(currentTheme.InfoPanelPendingFg)),
		labelStyle:   base.Foreground(lipgloss.Color(currentTheme.HeaderFg)).Bold(true),
		elapsedStyle: dim,
		stopStyle:    dim,
		gapStyle:     base,
	}
}

// jobRowLayout is one rendered job row plus where its stop affordance sits
// relative to the row's left edge, so callers can map clicks back to the job
// after adding their own indentation.
type jobRowLayout struct {
	text          string
	stoppable     bool
	stopZoneStart int
	stopZoneEnd   int
}

// renderJobRow renders "<status dot> <label> <elapsed> x". The label is
// truncated last: the elapsed field and the stop affordance are reserved first,
// so they stay visible however long the label is.
func renderJobRow(contentWidth int, job tools.JobState, now time.Time, styles jobRowStyles) jobRowLayout {
	if contentWidth <= 0 {
		return jobRowLayout{}
	}
	stoppable := job.Status == jobStatusRunning
	indicatorStatus := job.Status
	if job.Status == jobStatusStopping {
		// statusIndicator names the retrying family, not the job lifecycle state.
		indicatorStatus = "retrying"
	}
	dot := statusIndicator(indicatorStatus, false)
	elapsed := tools.FormatElapsed(now.Sub(job.StartedAt))
	// FormatElapsed has no upper bound ("1000h02m03s" and beyond), so clamp the
	// value: an unbounded one would widen the row and push the stop affordance
	// out of its hit zone.
	if ansi.StringWidth(elapsed) > jobElapsedColumnWidth {
		elapsed = truncateOneLine(elapsed, jobElapsedColumnWidth)
	}
	elapsedWidth := ansi.StringWidth(elapsed)

	label := job.Description
	if strings.TrimSpace(label) == "" {
		label = job.Command
	}
	label = sanitizeToolDisplayText(label)

	reserved := ansi.StringWidth(dot) + 1 + 1 + elapsedWidth
	if stoppable {
		reserved += 1 + ansi.StringWidth(jobStopGlyph) + 1
	}
	availLabel := max(contentWidth-reserved, 0)
	if availLabel > 0 {
		label = truncateOneLine(label, availLabel)
	} else {
		label = ""
	}
	labelPad := max(availLabel-ansi.StringWidth(label), 0)

	var b strings.Builder
	b.WriteString(styles.dotStyle.Render(dot))
	b.WriteString(styles.gapStyle.Render(" "))
	b.WriteString(styles.labelStyle.Render(label))
	if labelPad > 0 {
		b.WriteString(styles.gapStyle.Render(strings.Repeat(" ", labelPad)))
	}
	b.WriteString(styles.gapStyle.Render(" "))
	b.WriteString(styles.elapsedStyle.Render(elapsed))
	row := jobRowLayout{stoppable: stoppable}
	if stoppable {
		// The trailing gap keeps the glyph off the row's last column, the same
		// spare cell CHANGED FILES leaves after its stats.
		b.WriteString(styles.gapStyle.Render(" "))
		b.WriteString(styles.stopStyle.Render(jobStopGlyph))
		b.WriteString(styles.gapStyle.Render(" "))
		row.stopZoneStart = max(contentWidth-jobStopZoneCells, 0)
		row.stopZoneEnd = contentWidth
	}
	row.text = b.String()
	return row
}
