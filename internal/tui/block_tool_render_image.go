package tui

import (
	"encoding/json"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func toolIsImageCard(name string) bool {
	return name == tools.NameGenerateImage || name == tools.NameViewImage
}

// Image cards present prompts and attachments, while the complete tool result remains
// available to transcript persistence, model input, copying and delivery.
func (b *Block) renderImageToolCard(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	_, vals := b.toolArgsParsed()
	var subject, mainPart, grayPart string
	if b.ToolName == tools.NameViewImage {
		subject = strings.TrimSpace(vals["label"])
		if subject == "" {
			subject = b.displayToolPath(vals["path"])
		}
	} else {
		mainPart = firstDisplayLine(strings.TrimSpace(vals["prompt"]))
		grayPart = strings.TrimSpace(vals["operation"])
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	if b.IsError {
		prefix = "✗"
	}
	if b.RecoveryState == message.ToolRecoveryStateOutcomeUnknown {
		prefix = "!"
	}
	header := renderToolHeaderLine(prefix, b.ToolName)
	header = appendToolHeaderSummary(header, mainPart, grayPart, subject, metrics.cardWidth-4)
	header = buildToolHeaderLine(header, b.ToolProgress, metrics.cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	body := []string{header}
	content := toolDisplayResultContent(b)
	if content == "" {
		content = b.Content
	}
	switch {
	case b.RecoveryState == message.ToolRecoveryStateOutcomeUnknown:
		body = append(body, toolFieldSection(LSPWarnStyle, "Result unknown"))
		appendStyledWrappedBody(&body, LSPWarnStyle, "    ", content, metrics.contentWidth)
	case b.IsError || b.toolResultIsError():
		appendToolOutcomeBody(&body, toolOutcomeError, content, metrics.contentWidth, true)
	case b.toolResultIsCancelled():
		appendToolOutcomeBody(&body, toolOutcomeCancelled, content, metrics.contentWidth, true)
	default:
		if b.ToolName == tools.NameGenerateImage && b.ResultDone {
			var summary tools.ImageGenerationSummary
			if json.Unmarshal([]byte(content), &summary) == nil {
				for _, warning := range summary.Warnings {
					if strings.TrimSpace(warning) != "" {
						body = append(body, toolFieldSection(LSPWarnStyle, "Warning"))
						appendStyledWrappedBody(&body, LSPWarnStyle, "    ", warning, metrics.contentWidth)
					}
				}
			}
		} else if note := viewImageScalingNote(content); note != "" {
			appendStyledWrappedBody(&body, DimStyle, "    ", note, metrics.contentWidth)
		}
	}
	if !b.appendImagePreviewLines(&body, metrics.contentWidth, metrics.toolCardBg, metrics.blockStyle.GetPaddingTop(), true) && b.ResultDone && !b.IsError && !b.toolResultIsError() && !b.toolResultIsCancelled() && b.RecoveryState != message.ToolRecoveryStateOutcomeUnknown {
		body = append(body, toolFieldStandalone(DimStyle, "Image preview unavailable"))
	}
	body = appendToolElapsedToHeader(body, b, metrics.cardWidth)
	title := "TOOL CALL"
	if b.Type == BlockToolResult {
		title = "TOOL RESULT"
	}
	return b.renderToolCardWithIgnoredArgs(metrics.blockStyle, metrics.cardWidth, toolCardTitle(title, b.displayLabelID()), body, metrics.toolCardBg, cardRailPrefix("tool", b.Focused))
}

// view_image reports normalization in its text result. Keep the size change
// when detail was lost, without repeating the load confirmation or filename.
func viewImageScalingNote(content string) string {
	if !strings.HasPrefix(content, "Loaded image ") || !strings.HasSuffix(content, ").") {
		return ""
	}
	start := strings.LastIndex(content, "\" into context (")
	if start < 0 {
		return ""
	}
	size := content[start+len("\" into context (") : len(content)-2]
	if !strings.Contains(size, " -> ") {
		return ""
	}
	return size
}
