package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

const (
	// stopJobPeekTailBytes bounds the tail copied into the dialog when it opens;
	// the tail is frozen at that point so a chatty job cannot make the dialog
	// jump around while the operator reads it.
	stopJobPeekTailBytes = 64 * 1024
	// stopJobTailLines is how many trailing output lines the dialog shows.
	stopJobTailLines = 12
	// stopJobCommandMaxLines caps the wrapped command preview before an ellipsis.
	stopJobCommandMaxLines = 4
	// stopJobLivePeekTailBytes is deliberately tiny: the once-per-second refresh
	// needs only StartedAt and LastOutputAt, while PeekJobForDisplay always
	// materializes a cleaned tail — requesting 0 ("no limit") would clean the
	// whole retained window every second.
	stopJobLivePeekTailBytes = 1
	// stopJobConfirmReason is the fixed reason recorded on an operator stop; the
	// job's status text renders it as "killed (stopped by user)".
	stopJobConfirmReason = "stopped by user"
)

// stopJobConfirmState is the operator's stop-job dialog. The tail, label, and
// command are frozen when the dialog opens; only the elapsed time and the last
// output timestamp refresh while it stays open.
type stopJobConfirmState struct {
	jobID        string
	label        string
	command      string
	owner        string
	status       string
	startedAt    time.Time
	lastOutputAt time.Time
	logFile      string
	tail         string
	droppedBytes int64
	truncated    bool
	prevMode     Mode
	openedAt     time.Time
	// lastLiveAt throttles the once-per-second elapsed/last-output refresh.
	lastLiveAt time.Time

	renderCacheWidth int
	renderCacheTheme string
	renderCacheText  string
}

// openStopJobConfirm opens the confirmation for one job. It is a no-op toast
// when the job is already gone, so a stale click never opens a dialog that
// cannot act.
func (m *Model) openStopJobConfirm(jobID string) tea.Cmd {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil
	}
	peek, ok := tools.PeekJobForDisplay(jobID, stopJobPeekTailBytes)
	if !ok || (peek.Status != jobStatusRunning && peek.Status != jobStatusStopping) {
		// A stale hit box can point at a job that finished since the panel was
		// drawn; never open a dialog for a job that is already terminal.
		return m.enqueueToast(fmt.Sprintf("Job %s is no longer running", jobID), "info")
	}
	openedAt := time.Now()
	m.stopJobConfirm = stopJobConfirmState{
		jobID:        peek.ID,
		label:        sanitizeToolDisplayText(peek.Label),
		command:      sanitizeToolDisplayText(peek.Command),
		owner:        sanitizeToolDisplayText(peek.Owner),
		status:       peek.Status,
		startedAt:    peek.StartedAt,
		lastOutputAt: peek.LastOutputAt,
		logFile:      peek.LogFile,
		tail:         peek.Tail,
		droppedBytes: peek.DroppedBytes,
		truncated:    peek.Truncated,
		prevMode:     m.mode,
		openedAt:     openedAt,
	}
	cmd := m.switchModeWithIME(ModeStopJobConfirm)
	m.recalcViewportSize()
	return cmd
}

// active reports whether a stop-job dialog is installed. Closing clears the
// state before the queued dialogs are replayed, so this — not the current mode —
// is what dialogActive keys on.
func (s stopJobConfirmState) active() bool {
	return s.jobID != ""
}

// closeStopJobConfirm closes only this dialog, restoring the mode that was
// active when it opened (the jobs overlay included), never cascading, and
// replaying any dialogs that queued up meanwhile.
func (m *Model) closeStopJobConfirm() tea.Cmd {
	if m.mode != ModeStopJobConfirm {
		return nil
	}
	prevMode := m.stopJobConfirm.prevMode
	m.stopJobConfirm = stopJobConfirmState{}
	// The dialog counted as a user-response request for the terminal title;
	// clear that marker and re-sync so the ❓ does not survive the close.
	m.terminalTitleRequestSeen = false
	m.recalcViewportSize()
	return m.finishDialog(prevMode, m.syncTerminalTitleState())
}

func (m *Model) handleStopJobConfirmKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		return m.confirmStopJob()
	case "n", "N", "esc":
		return m.closeStopJobConfirm()
	default:
		// enter is deliberately inert: stopping a background job must be an
		// explicit y.
		return nil
	}
}

func (m *Model) confirmStopJob() tea.Cmd {
	jobID := m.stopJobConfirm.jobID
	closeCmd := m.closeStopJobConfirm()
	if jobID == "" {
		return closeCmd
	}
	if !tools.StopJobByUser(jobID, stopJobConfirmReason) {
		return tea.Batch(closeCmd, m.enqueueToast(fmt.Sprintf("Job %s is no longer running", jobID), "info"))
	}
	return tea.Batch(closeCmd, m.syncJobAnimation())
}

// refreshStopJobConfirmLive updates the two fields that keep moving while the
// dialog is open. The tail and the labels stay frozen, so a busy job cannot make
// the text the operator is reading shift under them.
func (m *Model) refreshStopJobConfirmLive(now time.Time) {
	if m.mode != ModeStopJobConfirm || m.stopJobConfirm.jobID == "" {
		return
	}
	// The first refresh is due one second after the dialog opened; later ones
	// anchor on the previous refresh.
	baseline := m.stopJobConfirm.lastLiveAt
	if baseline.IsZero() {
		baseline = m.stopJobConfirm.openedAt
	}
	if now.Sub(baseline) < time.Second {
		return
	}
	m.stopJobConfirm.lastLiveAt = now
	peek, ok := tools.PeekJobForDisplay(m.stopJobConfirm.jobID, stopJobLivePeekTailBytes)
	if !ok {
		return
	}
	m.stopJobConfirm.startedAt = peek.StartedAt
	m.stopJobConfirm.lastOutputAt = peek.LastOutputAt
	m.stopJobConfirm.renderCacheText = ""
}

func (m *Model) renderStopJobConfirmDialog() string {
	s := &m.stopJobConfirm
	if s.jobID == "" {
		return ""
	}
	if s.renderCacheText != "" &&
		s.renderCacheWidth == m.width &&
		s.renderCacheTheme == m.theme.Name {
		return s.renderCacheText
	}
	const maxDialogWidth = 90
	maxWidth := max(min(m.width-6, maxDialogWidth), 40)
	innerWidth := dialogContentWidth(maxWidth)
	now := time.Now()

	lines := []string{ConfirmSeparatorStyle.Render(truncateOneLine("⚠ Stop "+s.jobID+"?", innerWidth))}
	if s.label != "" {
		lines = append(lines, stopJobInlineField("Label", s.label, innerWidth))
	}
	lines = append(lines, ConfirmToolStyle.Render("Command:"))
	command := strings.TrimSpace(s.command)
	if command == "" {
		command = "(none)"
	}
	commandLines := wrapText(command, innerWidth)
	if len(commandLines) > stopJobCommandMaxLines {
		commandLines = append(commandLines[:stopJobCommandMaxLines:stopJobCommandMaxLines], "…")
	}
	lines = append(lines, commandLines...)
	lines = append(lines,
		stopJobInlineField("Owner", stopJobFallback(s.owner, "(unknown)"), innerWidth),
		stopJobInlineField("Status", stopJobFallback(s.status, "(unknown)"), innerWidth),
		stopJobInlineField("Elapsed", tools.FormatElapsed(now.Sub(s.startedAt)), innerWidth),
		stopJobInlineField("Last output", stopJobLastOutputValue(s.lastOutputAt, now), innerWidth),
		"",
		ConfirmToolStyle.Render("Recent output (last 12 lines):"),
	)
	if tailLines := stopJobTailPreview(s.tail, innerWidth); len(tailLines) > 0 {
		lines = append(lines, tailLines...)
	} else {
		lines = append(lines, DimStyle.Render("(no output yet)"))
	}
	if notice := stopJobTruncationNotice(s.droppedBytes, s.truncated, s.logFile); notice != "" {
		// The notice carries a full log path, which can exceed the dialog width;
		// padLineToDisplayWidthWithStyle only pads, so it must be clamped here.
		lines = append(lines, DimStyle.Render(truncateOneLine(notice, innerWidth)))
	}
	lines = append(lines, "")
	lines = append(lines, wrapStyledLines(ConfirmDenyStyle, "Stopping sends SIGTERM, then SIGKILL after a grace period. Output captured so far is kept; the owner agent is notified when the job ends.", innerWidth)...)
	lines = append(lines, "",
		lipgloss.JoinHorizontal(lipgloss.Left,
			ConfirmAllowStyle.Render("[y] Stop"),
			DimStyle.Render("   "),
			ConfirmDenyStyle.Render("[n/esc] Cancel"),
		),
	)

	out := renderDialogBox(maxWidth, lines)
	s.renderCacheWidth = m.width
	s.renderCacheTheme = m.theme.Name
	s.renderCacheText = out
	return out
}

func stopJobInlineField(label, value string, innerWidth int) string {
	prefix := label + ": "
	value = truncateOneLine(value, max(innerWidth-ansi.StringWidth(prefix), 1))
	return ConfirmToolStyle.Render(prefix + value)
}

func stopJobFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func stopJobLastOutputValue(lastOutputAt, now time.Time) string {
	if lastOutputAt.IsZero() {
		return "(no output yet)"
	}
	ago := max(now.Sub(lastOutputAt), 0)
	return fmt.Sprintf("%s (%s ago)", lastOutputAt.Format("15:04:05"), tools.FormatElapsed(ago))
}

func stopJobTailPreview(tail string, innerWidth int) []string {
	raw := strings.Split(strings.TrimRight(tail, "\n"), "\n")
	if len(raw) == 1 && strings.TrimSpace(raw[0]) == "" {
		return nil
	}
	if len(raw) > stopJobTailLines {
		raw = raw[len(raw)-stopJobTailLines:]
	}
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		lines = append(lines, DimStyle.Render(ansi.Truncate(line, innerWidth, "…")))
	}
	return lines
}

// stopJobTruncationNotice mirrors job_output's dropped-bytes wording so the
// dialog and the model-facing read describe the same omission the same way.
func stopJobTruncationNotice(droppedBytes int64, truncated bool, logFile string) string {
	switch {
	case droppedBytes > 0:
		if logFile != "" {
			return fmt.Sprintf("(skipped %d bytes of earlier output; recent log: %s)", droppedBytes, logFile)
		}
		return fmt.Sprintf("(skipped %d bytes of earlier output)", droppedBytes)
	case truncated:
		if logFile != "" {
			return fmt.Sprintf("(showing the most recent output; recent log: %s)", logFile)
		}
		return "(showing the most recent output)"
	default:
		return ""
	}
}

// wrapStyledLines word-wraps text to innerWidth and applies style to each line,
// keeping every line inside the dialog's content width.
func wrapStyledLines(style lipgloss.Style, text string, innerWidth int) []string {
	wrapped := wrapText(text, max(innerWidth, 1))
	lines := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		lines = append(lines, style.Render(line))
	}
	return lines
}
