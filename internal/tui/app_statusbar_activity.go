package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keakon/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/bytefmt"
	"github.com/keakon/chord/internal/tools"
)

// formatStatusBarElapsed formats activity/shell elapsed time for the status bar
// as a primary inline value, without parentheses.
func formatStatusBarElapsed(d time.Duration) string {
	return " " + tools.FormatElapsed(d)
}

func statusBarIdleLabel() string {
	return "Since "
}

func statusBarStartedLabel() string {
	return "Since "
}

func formatStatusBarStartedAt(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return statusBarStartedLabel() + t.Format("15:04")
}

// statusBarWaitRemaining reports how much of a wait with a known deadline is
// left. The runtime attaches a deadline to a cooling wait (the key recovery
// instant) and to a retry round that pauses before its next attempt; without
// one the lane falls back to the elapsed phase time.
func statusBarWaitRemaining(a agent.AgentActivityEvent, now time.Time) (time.Duration, bool) {
	if a.Deadline.IsZero() {
		return 0, false
	}
	return max(a.Deadline.Sub(now), 0), true
}

// formatStatusBarCountdown renders a remaining wait at readable granularity:
// whole seconds below a minute, minutes below an hour, then hours and minutes.
// A provider quota reset can be hours out, so the wait does reach the hour form.
//
// The trailing field is zero-padded, matching tools.FormatElapsed's convention:
// a countdown repaints every second, and an unpadded field would shift the digit
// position on every tick that crosses a power of ten (1m10s -> 1m9s).
func formatStatusBarCountdown(d time.Duration) string {
	d = ceilDuration(max(d, 0), time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		minutes := int(d / time.Minute)
		seconds := int((d % time.Minute) / time.Second)
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	// Seconds are noise at this range: an hours-long wait is read as "about
	// how long until I can work again", and a ticking seconds field would
	// force a status-bar repaint every second for no added information.
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%02dm", hours, minutes)
}

func formatStatusBarBytes(n int64) string {
	return bytefmt.Compact(n)
}

func formatStatusBarEvents(n int64) string {
	if n <= 0 {
		return ""
	}
	label := "events"
	if n == 1 {
		label = "event"
	}
	return fmt.Sprintf("%d %s", n, label)
}

func formatStatusBarTransportProgress(bytes, events int64) string {
	progress := "↓ " + formatStatusBarBytes(bytes)
	if formattedEvents := formatStatusBarEvents(events); formattedEvents != "" {
		progress += " · " + formattedEvents
	}
	return progress
}

func (m Model) renderRequestProgressSummary(agentID string) string {
	if agentID == "" {
		agentID = "main"
	}
	if status, ok := m.sidebar.FindStatus(agentID); ok && subAgentStatusSuspendsActivity(status) {
		return ""
	}
	prog, ok := m.requestProgress[agentID]
	displayBytes := int64(0)
	displayEvents := int64(0)
	if ok {
		displayBytes = max(prog.VisibleBytes-prog.BaseBytes, 0)
		displayEvents = max(prog.VisibleEvents-prog.BaseEvents, 0)
	}
	hasDownloadState := false
	if act, ok := m.activities[agentID]; ok {
		hasDownloadState = act.Type == agent.ActivityWaitingHeaders || act.Type == agent.ActivityWaitingToken || act.Type == agent.ActivityStreaming
	}
	if !hasDownloadState && (!ok || prog.VisibleBytes <= 0) {
		return ""
	}
	summary := formatStatusBarTransportProgress(displayBytes, displayEvents)
	if start, ok := m.activityStartTime[statusBarTimingAnchor(agentID)]; ok && !start.IsZero() {
		summary += " · " + strings.TrimSpace(formatStatusBarElapsed(time.Since(start)))
	}
	return summary
}

func (m Model) executingStartedAt(agentID string) (time.Time, bool) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	if start, ok := m.activityStartTime[statusBarTimingAnchor(agentID)]; ok && !start.IsZero() {
		return start, true
	}
	if start, ok := m.activityStartTime[agentID]; ok && !start.IsZero() {
		return start, true
	}
	if t, ok := lastVisibleBlockStartedWall(m.viewport); ok {
		return t, true
	}
	return time.Time{}, false
}

func statusBarTimingAnchor(agentID string) string {
	if agentID == "" || agentID == "main" {
		return "main"
	}
	return agentID
}

func latestQueuedDraftWall(drafts []queuedDraft) (time.Time, bool) {
	for _, draft := range slices.Backward(drafts) {
		if t := draft.QueuedAt; !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
}

func latestVisibleStartWall(v *Viewport) (time.Time, bool) {
	return lastVisibleBlockStartedWall(v)
}

func (m *Model) latestStatusStartWall(agentID string) (time.Time, bool) {
	var latest time.Time
	if t, ok := latestVisibleStartWall(m.viewport); ok && t.After(latest) {
		latest = t
	}
	if t, ok := latestQueuedDraftWall(m.visibleQueuedDrafts()); ok && t.After(latest) {
		latest = t
	}
	if m.inflightDraftBelongsToAgent(agentID) {
		if t := m.inflightDraft.QueuedAt; !t.IsZero() && t.After(latest) {
			latest = t
		}
	}
	if t, ok := latestVisiblePendingUserLocalShellStartedWall(m.viewport); ok && t.After(latest) {
		latest = t
	}
	if t := m.workStartedAt[statusBarTimingAnchor(agentID)]; !t.IsZero() && t.After(latest) {
		latest = t
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	return latest, true
}

func latestVisiblePendingUserLocalShellStartedWall(v *Viewport) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	return v.LatestVisiblePendingUserLocalShellStartedAt()
}

func (m Model) renderStatusBarLocalShell(maxWidth int) string {
	startedAt, _ := latestVisiblePendingUserLocalShellStartedWall(m.viewport)
	elapsed := ""
	if !startedAt.IsZero() {
		elapsed = formatStatusBarElapsed(max(time.Now().Truncate(time.Second).Sub(startedAt), 0))
	}
	text := "Terminal" + elapsed
	started := ""
	if !startedAt.IsZero() {
		started = DimStyle.Render(" · " + formatStatusBarStartedAt(startedAt))
	}
	iconStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.AccentGradientToFg))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.StatusFg))
	out := iconStyle.Render("!") + " " + textStyle.Render(text) + started
	if maxWidth > 0 && lipgloss.Width(out) > maxWidth {
		short := iconStyle.Render("!") + " " + textStyle.Render("Terminal"+elapsed)
		if lipgloss.Width(short) <= maxWidth {
			out = short
		} else {
			out = runewidth.Truncate(out, maxWidth, "…")
		}
	}
	return out
}

type statusBarActivityDisplay struct {
	Icon string
	Text string
	// CompactText is a narrower variant of Text for tight status bar widths;
	// it keeps the most useful numeric payload before falling back to the icon.
	CompactText string
	// NarrowText is the last text variant tried before truncation. An empty value
	// means the icon alone is the honest narrowest representation.
	NarrowText string
}

// Fixed-width frames keep the footer stable across animation updates.
var statusBarConnectingFrames = [...]string{"⠋", "⠹", "⠴", "⠧"}

func (m Model) statusBarElapsedTextAt(agentID string, now time.Time) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	anchor := statusBarTimingAnchor(agentID)
	if start, ok := m.activityStartTime[anchor]; ok && !start.IsZero() {
		return strings.TrimSpace(formatStatusBarElapsed(now.Sub(start)))
	}
	if start, ok := m.activityStartTime[agentID]; ok && !start.IsZero() {
		return strings.TrimSpace(formatStatusBarElapsed(now.Sub(start)))
	}
	if start, ok := m.latestStatusStartWall(agentID); ok {
		return strings.TrimSpace(formatStatusBarElapsed(now.Sub(start)))
	}
	return "0s"
}

func (m Model) buildStatusBarActivityDisplayAt(a agent.AgentActivityEvent, now time.Time) statusBarActivityDisplay {
	display := statusBarActivityDisplay{}
	agentID := strings.TrimSpace(a.AgentID)
	if agentID == "" {
		agentID = "main"
	}

	elapsedText := m.statusBarElapsedTextAt(agentID, now)
	if a.Type == agent.ActivityExecuting {
		// The lane's icon names the activity kind, so executing keeps its own
		// glyph and the elapsed follows as plain text.
		display.Icon = executingGlyph
		if startedAt, ok := m.executingStartedAt(agentID); ok {
			display.Text = strings.TrimSpace(formatStatusBarElapsed(now.Sub(startedAt)))
		}
		return display
	}

	switch a.Type {
	case agent.ActivityPreparing:
		// Preparing can wait on an async readiness gate. Keep the compact footer
		// limited to the activity kind and elapsed time.
		display.Icon = "✶"
		display.Text = elapsedText
	case agent.ActivityConnecting:
		display.Icon = m.statusBarConnectingFrameAt(now)
		display.Text = elapsedText
	case agent.ActivityWaitingHeaders:
		display.Icon = "◷"
		display.Text = elapsedText
	case agent.ActivityWaitingToken:
		display.Icon = "◌"
		display.Text = elapsedText
	case agent.ActivityCompacting:
		display.Icon = compactionPillIconAt(now)
		display.Text = elapsedText
	case agent.ActivityRetrying:
		display.Icon = "↻"
		// A scheduled retry exposes only its countdown here. The detail explains
		// the provider/model/reason in the error surface, where it can be read
		// without widening the status bar.
		if remaining, ok := statusBarWaitRemaining(a, now); ok {
			display.Text = formatStatusBarCountdown(remaining)
			display.NarrowText = display.Text
		} else {
			display.Text = elapsedText
		}
	case agent.ActivityCooling:
		// A cooling wait has a known end, so the numeric payload is the remaining
		// time. The cause is recorded in the error surface, not repeated here.
		display.Icon = "⏸"
		if remaining, ok := statusBarWaitRemaining(a, now); ok {
			display.Text = formatStatusBarCountdown(remaining)
			display.NarrowText = display.Text
		} else {
			display.Text = elapsedText
		}
	case agent.ActivityRetryingKey:
		display.Icon = "⇄"
		display.Text = elapsedText
	case agent.ActivityStreaming:
		display.Icon = "↓"
		display.Text = elapsedText
		if progress, ok := m.requestProgress[agentID]; ok {
			bytes := max(progress.VisibleBytes-progress.BaseBytes, 0)
			events := max(progress.VisibleEvents-progress.BaseEvents, 0)
			if bytes > 0 || events > 0 {
				bytesText := formatStatusBarBytes(bytes)
				display.Text = bytesText + " · " + elapsedText
				display.CompactText = display.Text
				display.NarrowText = bytesText
				if eventsText := formatStatusBarEvents(events); eventsText != "" {
					display.Text = bytesText + " · " + eventsText + " · " + elapsedText
				}
			}
		}
	default:
		display.Icon = "▸"
		display.Text = elapsedText
	}
	return display
}

func (m *Model) renderActivityAt(a agent.AgentActivityEvent, maxWidth int, now time.Time) string {
	display := m.statusBarSampledActivityAt(a, now)
	icon := display.Icon
	text := display.Text

	iconColor := m.theme.AccentGradientToFg
	iconStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.StatusFg))

	out := iconStyle.Render(icon)
	if text != "" {
		out += " " + textStyle.Render(text)
	}
	if maxWidth > 0 && lipgloss.Width(out) > maxWidth && text != "" {
		if display.CompactText != "" {
			if compact := iconStyle.Render(icon) + " " + textStyle.Render(display.CompactText); lipgloss.Width(compact) <= maxWidth {
				return compact
			}
		}
		if display.NarrowText != "" {
			if narrow := iconStyle.Render(icon) + " " + textStyle.Render(display.NarrowText); lipgloss.Width(narrow) <= maxWidth {
				return narrow
			}
		}
		iconW := lipgloss.Width(iconStyle.Render(icon))
		if maxWidth <= iconW+1 {
			return iconStyle.Render(icon)
		}
		tw := max(maxWidth-iconW-1, 1)
		truncated := runewidth.Truncate(text, tw, "…")
		out = iconStyle.Render(icon) + " " + textStyle.Render(truncated)
	}
	return out
}

func (m Model) activityForAgent(agentID string) agent.AgentActivityEvent {
	activity := m.activities[agentID]
	if status, ok := m.sidebar.FindStatus(agentID); ok && subAgentStatusSuspendsActivity(status) {
		return agent.AgentActivityEvent{AgentID: agentID, Type: agent.ActivityIdle}
	}
	return activity
}

func (m Model) focusedAgentCanShowIdleSince() bool {
	focusedAgentID := m.focusedAgentIDOrMain()
	activity := m.activityForAgent(focusedAgentID)
	mainCompacting := focusedAgentID == "main" && m.compactionBgStatus.Active
	if (activity.Type != "" && activity.Type != agent.ActivityIdle) ||
		m.focusedAgentBusyForIdleSweep() || mainCompacting {
		return false
	}
	if progress, ok := m.requestProgress[focusedAgentID]; ok && !progress.Done {
		return false
	}
	_, ok := m.latestStatusStartWall(focusedAgentID)
	return ok
}

func (m Model) isFocusedAgentBusy() bool {
	statusActiveID := m.focusedAgentID
	if statusActiveID == "" {
		statusActiveID = "main"
	}
	if m.inflightDraftBelongsToAgent(statusActiveID) {
		return true
	}
	statusActivity := m.activityForAgent(statusActiveID)
	return statusActivity.Type != "" && statusActivity.Type != agent.ActivityIdle
}

// renderCompactionBackgroundPill creates the compaction background status pill.
// This renders a compact background pill with breathing animation and optional progress.
func (m *Model) renderCompactionBackgroundPill(now time.Time) string {
	status := m.statusBarSampledCompactionAt(now)
	if !compactionBackgroundStatusVisibleAt(status, now) {
		return ""
	}

	icon := compactionPillIconAt(now)
	if status.Terminal != "" {
		switch status.Terminal {
		case agent.CompactionStatusSucceeded:
			icon = "✓" // Checkmark for success
		case agent.CompactionStatusFailed:
			icon = "✗" // Cross for failure
		case agent.CompactionStatusSkipped:
			icon = "⤼" // Skip arrow: nothing was rewritten
		case agent.CompactionStatusCancelled:
			icon = "✕" // Void mark: the checkpoint or compaction was cancelled
		}
	}

	// Time elapsed since start
	elapsedText := strings.TrimSpace(formatStatusBarElapsed(now.Sub(status.StartedAt)))

	// Build pill content. The breathing icon stays the visual anchor in both
	// states: live compaction uses an alternating ■/▪ so it reads as still in
	// flight, terminal states use the outcome glyph (✓/✗/⤼/✕) for the short
	// flush window. Elapsed keeps ticking so a long compaction stays visibly
	// alive without a spinner.
	pillParts := make([]string, 0, 3)
	pillParts = append(pillParts, icon+" "+elapsedText)

	// A model-requested context checkpoint is labeled distinctly from a
	// usage-driven compaction so the user can tell the two apart.
	if status.Trigger == agent.CompactionTriggerModelDriven {
		pillParts = append(pillParts, "model checkpoint")
	}
	// Terminal reason (e.g. low-gain skip cause) is surfaced during the flush
	// window; the status bar truncates it to the available width.
	if status.Terminal != "" && status.Reason != "" {
		pillParts = append(pillParts, status.Reason)
	}

	// Show streaming progress as a bytes/events suffix. The compaction worker
	// reports cumulative response progress via CompactionStatusEvent; the suffix
	// makes a long compaction visibly alive without spinners. Header-only
	// progress carries bytes without events, so either counter alone is enough
	// to surface the suffix.
	if status.Bytes > 0 || status.Events > 0 {
		pillParts = append(pillParts, formatStatusBarTransportProgress(status.Bytes, status.Events))
	}

	// Handle terminal states (1-2s flush window)
	if status.Terminal != "" {
		return StatusHintStyle.Render(strings.Join(pillParts, " "))
	}

	return StatusHintStyle.Render(strings.Join(pillParts, " "))
}
