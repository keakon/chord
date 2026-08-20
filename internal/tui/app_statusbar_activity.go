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
)

func formatBusyTotalWall(d time.Duration) string {
	d = d.Round(time.Second)
	sec := int(d.Seconds())
	if sec < 60 {
		return ""
	}
	m := sec / 60
	s := sec % 60
	if m >= 60 {
		h := m / 60
		m = m % 60
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	return fmt.Sprintf("%dm%ds", m, s)
}

// formatStatusBarElapsed formats activity/shell elapsed time for the status bar
// as a primary inline value, without parentheses.
func formatStatusBarElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	sec := int(d.Seconds())
	if sec < 60 {
		return fmt.Sprintf(" %ds", sec)
	}
	return " " + formatBusyTotalWall(d)
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

func (m Model) renderExecutingSummary(agentID string) string {
	if agentID == "" {
		agentID = "main"
	}
	anchor := statusBarTimingAnchor(agentID)
	startedAt := m.activityStartTime[anchor]
	if startedAt.IsZero() {
		startedAt = m.activityStartTime[agentID]
	}
	if startedAt.IsZero() {
		if t, ok := lastVisibleBlockStartedWall(m.viewport); ok {
			startedAt = t
		}
	}
	if startedAt.IsZero() {
		return "⚙"
	}
	elapsed := max(time.Since(startedAt).Round(time.Second), time.Second)
	return "⚙ · " + elapsed.String()
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

func (m Model) statusBarExecutingElapsedText(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		agentID = "main"
	}
	if start, ok := m.activityStartTime[statusBarTimingAnchor(agentID)]; ok && !start.IsZero() {
		return strings.TrimSpace(formatStatusBarElapsed(time.Since(start)))
	}
	if start, ok := m.activityStartTime[agentID]; ok && !start.IsZero() {
		return strings.TrimSpace(formatStatusBarElapsed(time.Since(start)))
	}
	if t, ok := lastVisibleBlockStartedWall(m.viewport); ok {
		return strings.TrimSpace(formatStatusBarElapsed(time.Since(t)))
	}
	return "0s"
}

func (m Model) buildStatusBarActivityDisplayAt(a agent.AgentActivityEvent, now time.Time) statusBarActivityDisplay {
	display := statusBarActivityDisplay{}
	agentID := strings.TrimSpace(a.AgentID)
	if agentID == "" {
		agentID = "main"
	}

	elapsedText := m.statusBarElapsedText(agentID)

	prog, ok := m.requestProgress[agentID]
	hasRequestState := false
	if act, okAct := m.activities[agentID]; okAct {
		hasRequestState = act.Type == agent.ActivityConnecting || act.Type == agent.ActivityWaitingHeaders || act.Type == agent.ActivityWaitingToken || act.Type == agent.ActivityStreaming
	}
	if a.Type == agent.ActivityExecuting {
		display.Icon = "⚙"
		display.Text = m.statusBarExecutingElapsedText(agentID)
		return display
	}
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
	case agent.ActivityWaitingHeaders, agent.ActivityWaitingToken, agent.ActivityRetrying, agent.ActivityRetryingKey, agent.ActivityCooling:
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

func (m Model) renderActivity(a agent.AgentActivityEvent, maxWidth int) string {
	return m.renderActivityAt(a, maxWidth, time.Now())
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
		}
	}

	// Time elapsed since start
	elapsedText := strings.TrimSpace(formatStatusBarElapsed(now.Sub(m.compactionBgStatus.StartedAt)))

	// Build pill content
	pillParts := make([]string, 0, 2)
	pillParts = append(pillParts, icon+" "+elapsedText)

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
		return StatusHintStyle.Render(pillParts[0])
	}

	return StatusHintStyle.Render(strings.Join(pillParts, " "))
}
