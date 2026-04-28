package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/keakon/chord/internal/ratelimit"
)

func ceilDuration(d, unit time.Duration) time.Duration {
	if d <= 0 || unit <= 0 {
		return 0
	}
	q := d / unit
	if d%unit != 0 {
		q++
	}
	return q * unit
}

func nextDisplayTransition(d, unit time.Duration) time.Duration {
	if d <= 0 || unit <= 0 {
		return 0
	}
	ceil := ceilDuration(d, unit)
	next := d - (ceil - unit)
	if next <= 0 {
		return unit
	}
	return next
}

func nextRoundedDisplayTransition(d, unit time.Duration) time.Duration {
	if d <= 0 || unit <= 0 {
		return 0
	}
	rem := d % unit
	half := unit / 2
	switch {
	case rem == half:
		return time.Second
	case rem < half:
		return rem + half
	default:
		return rem - half
	}
}

// renderRateLimitBlock renders the RATE LIMIT info-panel section from a snapshot.
func (m *Model) renderRateLimitBlock(snap *ratelimit.KeyRateLimitSnapshot, lineW int) string {
	formatWindow := func(label string, w ratelimit.RateLimitWindow) string {
		pct := w.UsedPercent()
		valueParts := make([]string, 0, 2)
		if pct >= 0 {
			valueParts = append(valueParts, fmt.Sprintf("%.0f%%", pct))
		}
		if resetStr := formatRateLimitResetTime(w); resetStr != "" {
			valueParts = append(valueParts, resetStr)
		}
		if len(valueParts) == 0 {
			return ""
		}

		var valueStyle lipgloss.Style
		switch {
		case pct >= 95:
			valueStyle = InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelRateCriticalFg))
		case pct >= 80:
			valueStyle = InfoPanelValue.Foreground(lipgloss.Color(currentTheme.InfoPanelRateWarnFg))
		default:
			valueStyle = InfoPanelDim
		}

		text := strings.Join(valueParts, " ")
		if label != "" {
			text = label + ": " + text
		}
		return InfoPanelLineBg.Width(lineW).Render(valueStyle.Render(truncateOneLine(text, lineW-2)))
	}

	showPrimary := snap.Primary != nil
	showSecondary := snap.Secondary != nil && snap.Secondary.UsedPercent() != 0
	if !showPrimary && !showSecondary {
		return ""
	}
	showLabels := showPrimary && showSecondary

	lines := []string{InfoPanelLineBg.Width(lineW).Render(InfoPanelTitle.Render("RATE LIMIT"))}
	if snap.Primary != nil {
		label := ""
		if showLabels {
			label = formatRateLimitWindowLabel(snap.Primary.WindowMinutes, "Primary")
		}
		if row := formatWindow(label, *snap.Primary); row != "" {
			lines = append(lines, row)
		}
	}
	if showSecondary {
		label := ""
		if showLabels {
			label = formatRateLimitWindowLabel(snap.Secondary.WindowMinutes, "Secondary")
		}
		if row := formatWindow(label, *snap.Secondary); row != "" {
			lines = append(lines, row)
		}
	}
	if len(lines) == 1 {
		// No data to show beyond the title.
		return ""
	}
	return InfoPanelBlock.Width(lineW).Render(joinInfoPanelBlockLines(lines))
}

func formatRateLimitWindowLabel(windowMinutes int64, fallback string) string {
	if windowMinutes <= 0 {
		return fallback
	}

	window := time.Duration(windowMinutes) * time.Minute
	const week = 7 * 24 * time.Hour

	switch {
	case window%week == 0:
		return fmt.Sprintf("%dw", int(window/week))
	case window >= 24*time.Hour:
		days := int(window / (24 * time.Hour))
		hours := int((window % (24 * time.Hour)) / time.Hour)
		if hours == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, hours)
	case window >= time.Hour:
		hours := int(window / time.Hour)
		minutes := int((window % time.Hour) / time.Minute)
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", int(window/time.Minute))
	}
}

func formatRateLimitResetTime(w ratelimit.RateLimitWindow) string {
	if w.ResetsAt.IsZero() {
		return ""
	}

	d := time.Until(w.ResetsAt)
	if d < 0 {
		d = 0
	}

	if d < time.Minute {
		rounded := ceilDuration(d, time.Second)
		if rounded < time.Second {
			rounded = time.Second
		}
		return fmt.Sprintf("%ds", int(rounded/time.Second))
	}

	if d >= 24*time.Hour {
		rounded := d.Round(time.Hour)
		if rounded < 24*time.Hour {
			rounded = 24 * time.Hour
		}
		days := int(rounded / (24 * time.Hour))
		hours := int((rounded % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("%dd%dh", days, hours)
	}

	rounded := d.Round(time.Minute)
	if d > 0 && rounded < time.Minute {
		rounded = time.Minute
	}
	hours := int(rounded / time.Hour)
	minutes := int((rounded % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

func nextRateLimitWindowDisplayTransition(w *ratelimit.RateLimitWindow, now time.Time) time.Duration {
	if w == nil || w.ResetsAt.IsZero() {
		return 0
	}
	d := w.ResetsAt.Sub(now)
	if d <= 0 {
		return 0
	}
	switch {
	case d < time.Minute:
		return nextDisplayTransition(d, time.Second)
	case d >= 24*time.Hour:
		return nextRoundedDisplayTransition(d, time.Hour)
	default:
		return nextRoundedDisplayTransition(d, time.Minute)
	}
}

func nextRateLimitSnapshotDisplayTransition(snap *ratelimit.KeyRateLimitSnapshot, now time.Time) time.Duration {
	if snap == nil {
		return 0
	}
	best := time.Duration(0)
	for _, window := range []*ratelimit.RateLimitWindow{snap.Primary, snap.Secondary} {
		d := nextRateLimitWindowDisplayTransition(window, now)
		if d > 0 && (best == 0 || d < best) {
			best = d
		}
	}
	return best
}

func formatLSPServerDiagSuffix(errors, warnings int) string {
	parts := make([]string, 0, 2)
	if errors > 0 {
		parts = append(parts, fmt.Sprintf("%d E", errors))
	}
	if warnings > 0 {
		parts = append(parts, fmt.Sprintf("%d W", warnings))
	}
	return strings.Join(parts, ", ")
}

// renderLSPDiagSummary colorizes a diagnostics summary string like "3 E, 1 W" or "—".
// Tokens containing "E" get red, "W" get orange, everything else uses InfoPanelValue.
func renderLSPDiagSummary(s string) string {
	if s == "" || s == "—" {
		return InfoPanelValue.Render(s)
	}
	parts := strings.Split(s, ", ")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		switch {
		case strings.Contains(part, " E") || strings.HasSuffix(part, "E"):
			result = append(result, InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelDiagErrorFg)).Render(part))
		case strings.Contains(part, " W") || strings.HasSuffix(part, "W"):
			result = append(result, InfoPanelDim.Foreground(lipgloss.Color(currentTheme.InfoPanelDiagWarnFg)).Render(part))
		default:
			result = append(result, InfoPanelValue.Render(part))
		}
	}
	return strings.Join(result, InfoPanelDim.Render(", "))
}
