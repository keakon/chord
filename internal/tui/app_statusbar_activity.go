package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
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

func statusBarIdleLabel(compact bool) string {
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
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(n)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", n, units[idx])
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, units[idx])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
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

func (m Model) renderRequestProgressSummary(agentID string) string {
	if agentID == "" {
		agentID = "main"
	}
	prog, ok := m.requestProgress[agentID]
	displayBytes := int64(0)
	displayEvents := int64(0)
	if ok {
		displayBytes = prog.VisibleBytes - prog.BaseBytes
		if displayBytes < 0 {
			displayBytes = 0
		}
		displayEvents = prog.VisibleEvents - prog.BaseEvents
		if displayEvents < 0 {
			displayEvents = 0
		}
	}
	hasDownloadState := false
	if act, ok := m.activities[agentID]; ok {
		hasDownloadState = act.Type == agent.ActivityWaitingHeaders || act.Type == agent.ActivityWaitingToken || act.Type == agent.ActivityStreaming
	}
	if !hasDownloadState && (!ok || prog.VisibleBytes <= 0) {
		return ""
	}
	summary := "↓ " + formatStatusBarBytes(displayBytes)
	if events := formatStatusBarEvents(displayEvents, false); events != "" {
		summary += " · " + events
	}
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
	elapsed := time.Since(startedAt).Round(time.Second)
	if elapsed < time.Second {
		elapsed = time.Second
	}
	return "⚙ · " + elapsed.String()
}

func statusBarTimingAnchor(agentID string) string {
	if agentID == "" || agentID == "main" {
		return "main"
	}
	return agentID
}

func latestQueuedDraftWall(drafts []queuedDraft) (time.Time, bool) {
	for i := len(drafts) - 1; i >= 0; i-- {
		if t := drafts[i].QueuedAt; !t.IsZero() {
			return t, true
		}
	}
	return time.Time{}, false
}

func latestVisibleStartWall(v *Viewport) (time.Time, bool) {
	return lastVisibleBlockStartedWall(v)
}

func (m Model) latestStatusStartWall(agentID string) (time.Time, bool) {
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
	if t := m.localShellStartedAt; !t.IsZero() && t.After(latest) {
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

func (m Model) renderStatusBarLocalShell(maxWidth int) string {
	elapsed := ""
	if !m.localShellStartedAt.IsZero() {
		elapsed = formatStatusBarElapsed(time.Since(m.localShellStartedAt))
	}
	text := "Shell" + elapsed
	started := ""
	if !m.localShellStartedAt.IsZero() {
		started = DimStyle.Render(" · " + formatStatusBarStartedAt(m.localShellStartedAt))
	}
	iconStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(NeonAccentColor(1800 * time.Millisecond)))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.StatusFg))
	out := iconStyle.Render("!") + " " + textStyle.Render(text) + started
	if maxWidth > 0 && lipgloss.Width(out) > maxWidth {
		short := iconStyle.Render("!") + " " + textStyle.Render("Shell"+elapsed)
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

func (m Model) buildStatusBarActivityDisplay(a agent.AgentActivityEvent) statusBarActivityDisplay {
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
			bytes = prog.VisibleBytes - prog.BaseBytes
			if bytes < 0 {
				bytes = 0
			}
			events = prog.VisibleEvents - prog.BaseEvents
			if events < 0 {
				events = 0
			}
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
		if (time.Now().UnixMilli()/300)%2 == 0 {
			display.Icon = "■"
		} else {
			display.Icon = "▪"
		}
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

func (m Model) renderActivityState(a agent.AgentActivityEvent, maxWidth int) string {
	display := m.buildStatusBarActivityDisplay(a)
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
		tw := maxWidth - iconW - 1
		if tw < 1 {
			tw = 1
		}
		truncated := runewidth.Truncate(text, tw, "…")
		out = iconStyle.Render(icon) + " " + textStyle.Render(truncated)
	}
	return out
}

func (m Model) renderActivity(a agent.AgentActivityEvent, maxWidth int) string {
	return m.renderActivityState(a, maxWidth)
}

func (m Model) isFocusedAgentBusy() bool {
	statusActiveID := m.focusedAgentID
	if statusActiveID == "" {
		statusActiveID = "main"
	}
	if m.inflightDraftBelongsToAgent(statusActiveID) {
		return true
	}
	statusActivity := m.activities[statusActiveID]
	return statusActivity.Type != "" && statusActivity.Type != agent.ActivityIdle
}

// renderCompactionBackgroundPill creates the compaction background status pill.
// This renders a compact background pill with breathing animation and optional progress.
func (m *Model) renderCompactionBackgroundPill() string {
	if !m.compactionBgStatus.Active {
		return ""
	}

	// Base pill style with breathing animation
	icon := "■" // Solid block for active
	if m.compactionBgStatus.Terminal != "" {
		switch m.compactionBgStatus.Terminal {
		case "succeeded":
			icon = "✓" // Checkmark for success
		case "failed":
			icon = "✗" // Cross for failure
		case "cancelled":
			icon = "" // No icon for cancelled (immediate disappearance)
		}
	}

	// Time elapsed since start
	elapsed := time.Since(m.compactionBgStatus.StartedAt).Round(time.Second)
	elapsedText := fmt.Sprintf("%ds", int(elapsed.Seconds()))

	// Build pill content
	pillParts := make([]string, 0, 1)
	if icon != "" {
		pillParts = append(pillParts, icon+" "+elapsedText)
	} else {
		// For cancelled state, just show elapsed time briefly
		pillParts = append(pillParts, elapsedText)
	}

	// Dedicated compaction-progress events drive the optional suffix.
	if m.compactionBgStatus.Bytes > 0 || m.compactionBgStatus.Events > 0 {
		progress := ""
		if m.compactionBgStatus.Bytes > 0 {
			progress += fmt.Sprintf(" · ↓ %d KB", m.compactionBgStatus.Bytes/1024)
		}
		if m.compactionBgStatus.Events > 0 {
			progress += fmt.Sprintf(" · %d", m.compactionBgStatus.Events)
		}
		pillParts[0] += progress
	}

	// Handle terminal states (1-2s flush window)
	if m.compactionBgStatus.Terminal != "" {
		if time.Since(m.compactionBgStatus.TerminalAt) >= 2*time.Second {
			return ""
		}
		switch m.compactionBgStatus.Terminal {
		case "succeeded":
			return StatusHintStyle.Render(pillParts[0])
		case "failed":
			return StatusHintStyle.Render(pillParts[0])
		case "cancelled":
			// Cancelled disappears immediately
			return ""
		}
	}

	// Active state with breathing animation
	if icon == "■" {
		anim := time.Now().Truncate(300 * time.Millisecond)
		if anim.Truncate(600*time.Millisecond) == anim {
			icon = "▪" // Dashed block for breathing state
			if len(pillParts) > 0 {
				pillParts[0] = strings.Replace(pillParts[0], "■ ", "▪ ", 1)
			}
		}
	}

	return StatusHintStyle.Render(pillParts[0])
}
