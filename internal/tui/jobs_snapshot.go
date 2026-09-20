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
// two mean the job is still live; the snapshot's activity filter asks tools
// (tools.JobState.Active) instead of re-deriving that from these names, which
// stay for the TUI's own rendering checks: whether a row still carries the stop
// affordance, and whether a stop dialog may target the job.
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
	// jobQuietColumnWidth caps the compact quiet-duration field so a long
	// silent job cannot push the stop affordance out of its hit zone: the
	// widest prefix JobQuietLabel can carry plus the elapsed clamp.
	jobQuietColumnWidth = tools.JobQuietLabelPrefixWidth + jobElapsedColumnWidth
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
		if state.Active() {
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
	labelStyle   lipgloss.Style
	elapsedStyle lipgloss.Style
	stopStyle    lipgloss.Style
	gapStyle     lipgloss.Style
}

func infoPanelJobRowStyles() jobRowStyles {
	return jobRowStyles{
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
		labelStyle:   base.Foreground(lipgloss.Color(currentTheme.HeaderFg)).Bold(true),
		elapsedStyle: dim,
		stopStyle:    dim,
		gapStyle:     base,
	}
}

// selectedJobRowStyles is dialogJobRowStyles on the selection surface. A job
// row is a run of separately rendered segments and every one sets its own
// background, so an outer SelectedStyle wrapper is overdrawn by the row itself;
// a selected row has to carry the selection colors on each segment.
func selectedJobRowStyles() jobRowStyles {
	base := lipgloss.NewStyle().
		Background(lipgloss.Color(currentTheme.SelectedBg)).
		Foreground(lipgloss.Color(currentTheme.SelectedFg))
	return jobRowStyles{
		labelStyle:   base.Bold(true),
		elapsedStyle: base,
		stopStyle:    base,
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

// renderJobRow renders "<label> [<quiet> ]<elapsed>[ x]", with the quiet field
// only when includeQuiet is true. The label is truncated last: visible timing
// fields and the stop affordance stay available however long the label is.
func renderJobRow(contentWidth int, job tools.JobState, now time.Time, styles jobRowStyles, includeQuiet bool) jobRowLayout {
	if contentWidth <= 0 {
		return jobRowLayout{}
	}
	stoppable := job.Status == jobStatusRunning
	stopGlyphWidth := ansi.StringWidth(jobStopGlyph)
	stopWidth := 0
	row := jobRowLayout{stoppable: stoppable}
	if stoppable {
		stopWidth = 2 + stopGlyphWidth
		row.stopZoneStart = max(contentWidth-jobStopZoneCells, 0)
		row.stopZoneEnd = contentWidth
	}
	// No status column: every running row would repeat the same circle, and the
	// only other state the panel lists (stopping) shows itself by the missing
	// stop affordance, so the columns go to the label instead.
	baseWidth := 1 + stopWidth
	if contentWidth < baseWidth {
		if stoppable {
			row.text = styles.gapStyle.Render(strings.Repeat(" ", max(contentWidth-stopGlyphWidth, 0))) + styles.stopStyle.Render(jobStopGlyph)
		} else {
			row.text = styles.gapStyle.Render(strings.Repeat(" ", contentWidth))
		}
		return row
	}
	elapsed := tools.FormatElapsed(job.Elapsed(now))
	// FormatElapsed is ASCII-only, so len() is its display width: an unbounded
	// value would widen the row and push the stop affordance out of its hit zone.
	if len(elapsed) > jobElapsedColumnWidth {
		elapsed = truncateOneLine(elapsed, jobElapsedColumnWidth)
	}
	if available := contentWidth - baseWidth; available == 0 {
		elapsed = ""
	} else if len(elapsed) > available {
		elapsed = truncateOneLine(elapsed, available)
	}
	elapsedWidth := len(elapsed)
	quiet := ""
	quietWidth := 0
	if includeQuiet {
		quiet = tools.JobQuietLabel(job, now)
		if len(quiet) > jobQuietColumnWidth {
			quiet = truncateOneLine(quiet, jobQuietColumnWidth)
		}
		quietWidth = len(quiet)
		if baseWidth+elapsedWidth+quietWidth+1 >= contentWidth {
			quiet = ""
			quietWidth = 0
		}
	}

	label := job.Description
	if strings.TrimSpace(label) == "" {
		label = job.Command
	}
	label = sanitizeToolDisplayText(label)

	reserved := baseWidth + elapsedWidth
	if quietWidth > 0 {
		reserved += quietWidth + 1
	}
	availLabel := max(contentWidth-reserved, 0)
	if availLabel > 0 {
		label = truncateOneLine(label, availLabel)
	} else {
		label = ""
	}
	labelPad := max(availLabel-ansi.StringWidth(label), 0)

	var b strings.Builder
	b.WriteString(styles.labelStyle.Render(label))
	if labelPad > 0 {
		b.WriteString(styles.gapStyle.Render(strings.Repeat(" ", labelPad)))
	}
	b.WriteString(styles.gapStyle.Render(" "))
	if quiet != "" {
		b.WriteString(styles.elapsedStyle.Render(quiet))
		b.WriteString(styles.gapStyle.Render(" "))
	}
	b.WriteString(styles.elapsedStyle.Render(elapsed))
	if stoppable {
		// The trailing gap keeps the glyph off the row's last column, the same
		// spare cell CHANGED FILES leaves after its stats.
		b.WriteString(styles.gapStyle.Render(" "))
		b.WriteString(styles.stopStyle.Render(jobStopGlyph))
		b.WriteString(styles.gapStyle.Render(" "))
	}
	row.text = b.String()
	return row
}
