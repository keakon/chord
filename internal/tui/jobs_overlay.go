package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	tea "github.com/keakon/bubbletea/v2"
)

// jobsOverlayState is the background jobs overlay: a scrollable list of every
// job still running, opened from the narrow-layout status pill. Rows expose the
// same stop affordance as the info panel.
type jobsOverlayState struct {
	prevMode     Mode
	scrollOffset int
	// cursor names the row Enter acts on. It exists because the overlay is the
	// only mouse-free way to reach a job: the info panel's stop affordance has
	// no keyboard cursor, so without a selected row an operator on a terminal
	// without mouse reporting could list jobs but never stop one.
	cursor int
	// lastLiveAt throttles the once-per-second elapsed refresh.
	lastLiveAt time.Time

	renderCacheW      int
	renderCacheH      int
	renderCacheScroll int
	renderCacheCursor int
	renderCacheTheme  string
	renderCacheText   string
}

func (m *Model) openJobsOverlay() tea.Cmd {
	if len(m.activeJobs()) == 0 {
		return m.enqueueToast("No background jobs are running", "info")
	}
	if m.mode == ModeJobsOverlay {
		return nil
	}
	prevMode := m.mode
	m.clearActiveSearch()
	if prevMode == ModeInsert {
		m.input.Blur()
	}
	m.clearChordState()
	m.jobsOverlay = jobsOverlayState{prevMode: prevMode}
	m.mode = ModeJobsOverlay
	m.recalcViewportSize()
	return nil
}

func (m *Model) closeJobsOverlay() tea.Cmd {
	if m.mode != ModeJobsOverlay {
		return nil
	}
	prevMode := m.jobsOverlay.prevMode
	m.jobsOverlay = jobsOverlayState{}
	cmd := m.restoreModeWithIME(prevMode)
	m.recalcViewportSize()
	if prevMode == ModeInsert {
		return tea.Batch(cmd, m.input.Focus())
	}
	return cmd
}

func (m *Model) handleJobsOverlayKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "j", "down":
		m.moveJobsOverlayCursor(1)
	case "k", "up":
		m.moveJobsOverlayCursor(-1)
	case "ctrl+f":
		m.moveJobsOverlayCursor(m.jobsOverlayVisibleRows())
	case "ctrl+b":
		m.moveJobsOverlayCursor(-m.jobsOverlayVisibleRows())
	case "g":
		m.jobsOverlay.cursor = 0
		m.jobsOverlay.scrollOffset = 0
	case "G":
		m.jobsOverlay.cursor = max(len(m.activeJobs())-1, 0)
		m.jobsOverlay.scrollOffset = m.jobsOverlayMaxScroll()
	case "enter":
		if jobID, ok := m.jobsOverlayCursorJobID(); ok {
			return m.openStopJobConfirm(jobID)
		}
	default:
		if m.overlayCloseKeyMatches(key, nil) {
			return m.closeJobsOverlay()
		}
	}
	m.clampJobsOverlayScroll()
	return nil
}

// jobsOverlayCursorJobID returns the id of the selected row. Enter is the
// keyboard equivalent of clicking a row's stop affordance; a job that already
// finished is rejected downstream by openStopJobConfirm.
func (m *Model) jobsOverlayCursorJobID() (string, bool) {
	jobs := m.activeJobs()
	idx := m.jobsOverlay.cursor
	if idx < 0 || idx >= len(jobs) {
		return "", false
	}
	return jobs[idx].ID, true
}

func (m *Model) jobsOverlayMaxWidth() int {
	return max(min(m.width-12, 110), 60)
}

func (m *Model) jobsOverlayInnerWidth() int {
	return max(m.jobsOverlayMaxWidth()-4, 1)
}

func (m *Model) jobsOverlayVisibleRows() int {
	return max(m.height-12, 4)
}

func (m *Model) jobsOverlayMaxScroll() int {
	return max(len(m.activeJobs())-m.jobsOverlayVisibleRows(), 0)
}

func (m *Model) clampJobsOverlayScroll() {
	if m.jobsOverlay.scrollOffset < 0 {
		m.jobsOverlay.scrollOffset = 0
	}
	if maxScroll := m.jobsOverlayMaxScroll(); m.jobsOverlay.scrollOffset > maxScroll {
		m.jobsOverlay.scrollOffset = maxScroll
	}
	// The cursor names the row Enter acts on, so it must stay on a row that
	// still exists: jobs finish on their own and shrink the list underneath it.
	if last := len(m.activeJobs()) - 1; m.jobsOverlay.cursor > last {
		m.jobsOverlay.cursor = last
	}
	if m.jobsOverlay.cursor < 0 {
		m.jobsOverlay.cursor = 0
	}
}

// moveJobsOverlayCursor moves the selected row and scrolls it into view, so
// Enter always acts on a row the operator can see.
func (m *Model) moveJobsOverlayCursor(delta int) {
	jobs := m.activeJobs()
	if len(jobs) == 0 {
		m.jobsOverlay.cursor = 0
		return
	}
	m.jobsOverlay.cursor = min(max(m.jobsOverlay.cursor+delta, 0), len(jobs)-1)
	m.ensureJobsOverlayCursorVisible()
}

func (m *Model) ensureJobsOverlayCursorVisible() {
	visible := m.jobsOverlayVisibleRows()
	if visible <= 0 {
		return
	}
	start := m.jobsOverlay.scrollOffset
	switch {
	case m.jobsOverlay.cursor < start:
		m.jobsOverlay.scrollOffset = m.jobsOverlay.cursor
	case m.jobsOverlay.cursor >= start+visible:
		m.jobsOverlay.scrollOffset = m.jobsOverlay.cursor - visible + 1
	}
}

// refreshJobsOverlayLive throttles the overlay's elapsed/last-output refresh to
// once per second; rows are otherwise rebuilt only when the job set changes.
func (m *Model) refreshJobsOverlayLive(now time.Time) {
	if m.mode != ModeJobsOverlay || len(m.activeJobs()) == 0 {
		return
	}
	if now.Sub(m.jobsOverlay.lastLiveAt) < time.Second {
		return
	}
	m.jobsOverlay.lastLiveAt = now
	m.jobsOverlay.renderCacheText = ""
}

func (m *Model) renderJobsOverlayDialog() string {
	jobs := m.activeJobs()
	innerWidth := m.jobsOverlayInnerWidth()
	visible := min(m.jobsOverlayVisibleRows(), len(jobs))
	start := 0
	if visible > 0 {
		start = max(min(m.jobsOverlay.scrollOffset, len(jobs)-visible), 0)
	}
	if m.jobsOverlay.renderCacheText != "" &&
		m.jobsOverlay.renderCacheW == m.width &&
		m.jobsOverlay.renderCacheH == m.height &&
		m.jobsOverlay.renderCacheScroll == start &&
		m.jobsOverlay.renderCacheCursor == m.jobsOverlay.cursor &&
		m.jobsOverlay.renderCacheTheme == m.theme.Name {
		return m.jobsOverlay.renderCacheText
	}
	styles := dialogJobRowStyles()
	now := time.Now()
	contentLines := make([]string, 0, max(visible, 1))
	if visible == 0 {
		contentLines = append(contentLines, DimStyle.Render("(no background jobs)"))
	}
	for i, job := range jobs[start : start+visible] {
		line := renderJobRow(innerWidth, job, now, styles).text
		// The selected row is tinted without adding a prefix: a "▸" column
		// would shift the row and move the stop affordance out of the hit zone
		// the click handler maps back.
		if start+i == m.jobsOverlay.cursor {
			line = SelectedStyle.Width(innerWidth).Render(line)
		}
		contentLines = append(contentLines, line)
	}
	content := strings.Join(contentLines, "\n")
	scroll := ""
	if maxScroll := m.jobsOverlayMaxScroll(); maxScroll > 0 {
		scroll = fmt.Sprintf("  %d/%d", start+visible, len(jobs))
	}
	// A fixed min=max width keeps the content width — and so the stop-zone
	// columns the click handler maps back — independent of the longest row.
	maxWidth := m.jobsOverlayMaxWidth()
	dialog, _ := RenderOverlay(OverlayConfig{
		Title:    "Background Jobs",
		Hint:     "j/k select  enter stop  esc close" + scroll,
		MinWidth: maxWidth,
		MaxWidth: maxWidth,
	}, content, len(contentLines), image.Rect(0, 0, m.width, m.height))
	m.jobsOverlay.renderCacheW = m.width
	m.jobsOverlay.renderCacheH = m.height
	m.jobsOverlay.renderCacheScroll = start
	m.jobsOverlay.renderCacheCursor = m.jobsOverlay.cursor
	m.jobsOverlay.renderCacheTheme = m.theme.Name
	m.jobsOverlay.renderCacheText = dialog
	return dialog
}

// jobsOverlayRowAt resolves an overlay click to a job row. inStopZone reports
// whether the click landed on the row's stop affordance; clicks elsewhere on a
// row select nothing.
func (m *Model) jobsOverlayRowAt(x, y int) (jobID string, inStopZone bool, ok bool) {
	jobs := m.activeJobs()
	if len(jobs) == 0 {
		return "", false, false
	}
	dialog := m.renderJobsOverlayDialog()
	if dialog == "" {
		return "", false, false
	}
	rect := centeredRect(m.ensureLayout().area, dialog)
	visible := min(m.jobsOverlayVisibleRows(), len(jobs))
	windowStart := 0
	if visible > 0 {
		windowStart = max(min(m.jobsOverlay.scrollOffset, len(jobs)-visible), 0)
	}
	idx, hit := overlayItemIndexAt(rect, y, 2, windowStart, visible)
	if !hit || idx < 0 || idx >= len(jobs) {
		return "", false, false
	}
	job := jobs[idx]
	// A click also selects: keyboard and mouse act on the same row, so the
	// cursor never stops a job the operator last pointed at something else.
	m.jobsOverlay.cursor = idx
	if job.Status != jobStatusRunning {
		return job.ID, false, true
	}
	innerWidth := m.jobsOverlayInnerWidth()
	contentLeft := rect.Min.X + DirectoryBorderStyle.GetBorderLeftSize() + DirectoryBorderStyle.GetPaddingLeft()
	stopStart := contentLeft + max(innerWidth-jobStopZoneCells, 0)
	stopEnd := contentLeft + innerWidth
	return job.ID, x >= stopStart && x < stopEnd, true
}
