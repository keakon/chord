package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
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
	return appendToolHeaderSuffix(headerLine, DimStyle.Render("  "+progressText), maxWidth)
}

func buildToolHeaderLine(headerLine string, progress *agent.ToolProgressSnapshot, cardWidth int, queuedByExecutionEvent bool, isRunning bool) string {
	headerLine = appendToolProgressSuffix(headerLine, progress, cardWidth-4)
	if queuedByExecutionEvent && !isRunning {
		headerLine = renderQueuedToolHeaderBadge(headerLine, cardWidth)
	}
	return headerLine
}

func (b *Block) streamingArgProgress(argsJSON string) *agent.ToolProgressSnapshot {
	if toolNameKey(b.ToolName) == tools.NameApplyPatch {
		return nil
	}
	count := b.streamingArguments(argsJSON).count()
	if count == 0 {
		return nil
	}
	return &agent.ToolProgressSnapshot{Text: fmt.Sprintf("%d chars received", count)}
}
