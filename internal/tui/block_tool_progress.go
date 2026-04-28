package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
)

func toolProgressLabelForCount(label string, count int64) string {
	label = strings.TrimSpace(label)
	if count == 1 && strings.HasSuffix(label, "s") {
		return strings.TrimSuffix(label, "s")
	}
	return label
}

func formatToolProgress(progress *agent.ToolProgressSnapshot) string {
	if progress == nil {
		return ""
	}
	if progress.Total > 0 && progress.Current > 0 {
		label := toolProgressLabelForCount(progress.Label, progress.Total)
		if label != "" {
			return fmt.Sprintf("%s / %s %s", formatUsageTokens(progress.Current), formatUsageTokens(progress.Total), label)
		}
		return fmt.Sprintf("%s / %s", formatUsageTokens(progress.Current), formatUsageTokens(progress.Total))
	}
	if progress.Current > 0 {
		label := toolProgressLabelForCount(progress.Label, progress.Current)
		if label != "" {
			return fmt.Sprintf("%s %s", formatUsageTokens(progress.Current), label)
		}
		return formatUsageTokens(progress.Current)
	}
	return strings.TrimSpace(progress.Text)
}

func appendToolProgressSuffix(headerLine string, progress *agent.ToolProgressSnapshot, maxWidth int) string {
	progressText := formatToolProgress(progress)
	if progressText == "" {
		return headerLine
	}
	suffix := DimStyle.Render("  " + progressText)
	if maxWidth <= 0 {
		return headerLine + suffix
	}
	if runewidth.StringWidth(stripANSI(headerLine+suffix)) <= maxWidth {
		return headerLine + suffix
	}
	suffixWidth := runewidth.StringWidth(stripANSI(suffix))
	if suffixWidth >= maxWidth {
		return headerLine
	}
	headerBudget := maxWidth - suffixWidth
	if headerBudget < 1 {
		return headerLine
	}
	truncatedHeader := ansi.Truncate(headerLine, headerBudget, "…")
	if runewidth.StringWidth(stripANSI(truncatedHeader+suffix)) <= maxWidth {
		return truncatedHeader + suffix
	}
	return headerLine
}

func inferToolArgProgress(toolName, argsJSON string) *agent.ToolProgressSnapshot {
	_ = toolName
	count := utf8.RuneCountInString(strings.TrimSpace(argsJSON))
	if count > 0 {
		return &agent.ToolProgressSnapshot{Text: fmt.Sprintf("%d chars received", count)}
	}
	return nil
}
