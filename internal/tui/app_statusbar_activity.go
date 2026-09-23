package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
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

func formatStatusBarEvents(n int64, short bool) string {
	if n <= 0 {
		return ""
	}
	label := "events"
	if n == 1 {
		label = "event"
	}
	if short {
		label = "ev"
	}
	return fmt.Sprintf("%d %s", n, label)
}

func formatStatusBarTransportProgress(bytes, events int64) string {
	progress := "↓ " + formatStatusBarBytes(bytes)
	if formattedEvents := formatStatusBarEvents(events, false); formattedEvents != "" {
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

func (m Model) renderExecutingSummary(agentID string) string {
	if agentID == "" {
		agentID = "main"
	}
	startedAt, ok := m.executingStartedAt(agentID)
	if !ok {
		return executingGlyph
	}
	return executingGlyph + " · " + tools.FormatElapsed(time.Since(startedAt))
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
		elapsed = formatStatusBarElapsed(time.Since(startedAt))
	}
	text := "Terminal" + elapsed
	started := ""
	if !startedAt.IsZero() {
		started = DimStyle.Render(" · " + formatStatusBarStartedAt(startedAt))
	}
	iconStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(NeonAccentColor(1800 * time.Millisecond)))
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
	// CompactText is a narrower variant of Text for tight status bar widths
	// (for example "33s left" for "keys cooling · 33s left"); it is preferred
	// over truncation.
	CompactText string
	// NarrowText is the last variant tried before truncation (for example "33s"),
	// for lanes whose compact form still carries a label that the narrowest
	// widths cannot afford.
	NarrowText string
}

func (m Model) statusBarElapsedText(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	anchor := statusBarTimingAnchor(agentID)
	if start, ok := m.activityStartTime[anchor]; ok && !start.IsZero() {
		return strings.TrimSpace(formatStatusBarElapsed(time.Since(start)))
	}
	if start, ok := m.activityStartTime[agentID]; ok && !start.IsZero() {
		return strings.TrimSpace(formatStatusBarElapsed(time.Since(start)))
	}
	if start, ok := m.latestStatusStartWall(agentID); ok {
		return strings.TrimSpace(formatStatusBarElapsed(time.Since(start)))
	}
	return "0s"
}

func (m Model) buildStatusBarActivityDisplayAt(a agent.AgentActivityEvent, now time.Time) statusBarActivityDisplay {
	display := statusBarActivityDisplay{}
	agentID := strings.TrimSpace(a.AgentID)
	if agentID == "" {
		agentID = "main"
	}

	elapsedText := ""
	prog, ok := m.requestProgress[agentID]
	hasRequestState := false
	if act, okAct := m.activities[agentID]; okAct {
		hasRequestState = act.Type == agent.ActivityConnecting || act.Type == agent.ActivityWaitingHeaders || act.Type == agent.ActivityWaitingToken || act.Type == agent.ActivityStreaming
	}
	if a.Type == agent.ActivityExecuting {
		// The lane's icon names the activity kind (⇋ connecting, ↓ streaming,
		// ■ compacting), so executing keeps its own glyph and the elapsed
		// follows as plain text instead of taking over the icon slot.
		display.Icon = executingGlyph
		if startedAt, ok := m.executingStartedAt(agentID); ok {
			display.Text = strings.TrimSpace(formatStatusBarElapsed(time.Since(startedAt)))
		}
		return display
	}
	elapsedText = m.statusBarElapsedText(agentID)
	if hasRequestState {
		display.Icon = "↓"
		bytes := int64(0)
		events := int64(0)
		if ok {
			bytes = max(prog.VisibleBytes-prog.BaseBytes, 0)
			events = max(prog.VisibleEvents-prog.BaseEvents, 0)
		}
		display.Text = formatStatusBarBytes(bytes)
		if e := formatStatusBarEvents(events, false); e != "" {
			display.Text += " · " + e
		}
		display.Text += " · " + elapsedText
		return display
	}

	switch a.Type {
	case agent.ActivityConnecting:
		display.Icon = "⇋"
		display.Text = elapsedText
	case agent.ActivityCompacting:
		display.Icon = compactionPillIconAt(now)
		display.Text = elapsedText
	case agent.ActivityRetrying:
		display.Icon = "↺"
		// The detail explains why the request is waiting (fallback: <model>
		// (<reason>), same key, round N) and outlives the toast that announced the
		// transition, which matters when the wait lasts tens of seconds.
		detail := strings.TrimSpace(a.Detail)
		if remaining, ok := statusBarWaitRemaining(a, now); ok {
			// A round with a scheduled pause knows when its next attempt goes
			// out; that is what the user is waiting for, while the elapsed phase
			// time only says how long the retry streak has run.
			countdown := "retry in " + formatStatusBarCountdown(remaining)
			display.Text = countdown
			if detail != "" {
				display.Text = detail + " · " + countdown
			}
			display.CompactText = countdown
			display.NarrowText = formatStatusBarCountdown(remaining)
		} else if detail != "" {
			display.Text = detail + " · " + elapsedText
		} else {
			display.Text = elapsedText
		}
	case agent.ActivityCooling:
		// A cooling wait has a known end, so its primary time semantics is the
		// remaining time rather than the elapsed phase time. The lane names the
		// cause as well: "33s left" alone does not say what is waiting.
		display.Icon = "↺"
		if remaining, ok := statusBarWaitRemaining(a, now); ok {
			countdown := formatStatusBarCountdown(remaining)
			display.Text = "cooling down · " + countdown + " left"
			display.CompactText = countdown + " left"
			display.NarrowText = countdown
		} else {
			display.Text = elapsedText
		}
	case agent.ActivityWaitingHeaders, agent.ActivityWaitingToken, agent.ActivityRetryingKey:
		display.Icon = "↺"
		display.Text = elapsedText
	case agent.ActivityStreaming:
		if (time.Now().UnixMilli()/300)%2 == 0 {
			display.Icon = "⣿"
		} else {
			display.Icon = "⣶"
		}
		display.Text = elapsedText
	default:
		display.Icon = "▸"
		display.Text = elapsedText
	}
	return display
}

func (m Model) renderActivityAt(a agent.AgentActivityEvent, maxWidth int, now time.Time) string {
	display := m.buildStatusBarActivityDisplayAt(a, now)
	icon := display.Icon
	text := display.Text

	iconColor := NeonAccentColor(1800 * time.Millisecond)
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
	if !compactionBackgroundStatusVisibleAt(m.compactionBgStatus, now) {
		return ""
	}

	icon := compactionPillIconAt(now)
	if m.compactionBgStatus.Terminal != "" {
		switch m.compactionBgStatus.Terminal {
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
	elapsedText := strings.TrimSpace(formatStatusBarElapsed(now.Sub(m.compactionBgStatus.StartedAt)))

	// Build pill content. The breathing icon stays the visual anchor in both
	// states: live compaction uses an alternating ■/▪ so it reads as still in
	// flight, terminal states use the outcome glyph (✓/✗/⤼/✕) for the short
	// flush window. Elapsed keeps ticking so a long compaction stays visibly
	// alive without a spinner.
	pillParts := make([]string, 0, 3)
	pillParts = append(pillParts, icon+" "+elapsedText)

	// A model-requested context checkpoint is labeled distinctly from a
	// usage-driven compaction so the user can tell the two apart.
	if m.compactionBgStatus.Trigger == agent.CompactionTriggerModelDriven {
		pillParts = append(pillParts, "model checkpoint")
	}
	// Terminal reason (e.g. low-gain skip cause) is surfaced during the flush
	// window; the status bar truncates it to the available width.
	if m.compactionBgStatus.Terminal != "" && m.compactionBgStatus.Reason != "" {
		pillParts = append(pillParts, m.compactionBgStatus.Reason)
	}

	// Show streaming progress as a bytes/events suffix. The compaction worker
	// reports cumulative response progress via CompactionStatusEvent; the suffix
	// makes a long compaction visibly alive without spinners. Header-only
	// progress carries bytes without events, so either counter alone is enough
	// to surface the suffix.
	if m.compactionBgStatus.Bytes > 0 || m.compactionBgStatus.Events > 0 {
		pillParts = append(pillParts, formatStatusBarTransportProgress(m.compactionBgStatus.Bytes, m.compactionBgStatus.Events))
	}

	// Handle terminal states (1-2s flush window)
	if m.compactionBgStatus.Terminal != "" {
		return StatusHintStyle.Render(strings.Join(pillParts, " "))
	}

	return StatusHintStyle.Render(strings.Join(pillParts, " "))
}
