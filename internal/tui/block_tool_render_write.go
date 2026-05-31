package tui

import (
	"encoding/json"
	"strings"
)

// renderWriteCall renders a Write tool call result with a syntax-highlighted
// preview of the written file content.
func (b *Block) renderWriteCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth, codeWidth := numberedToolPreviewWidths(cardWidth)

	var filePath string
	var parsed struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(b.Content), &parsed) == nil {
		filePath = b.displayToolPath(parsed.Path)
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if filePath != "" {
		headerLine += " " + DimStyle.Render(filePath)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if b.Collapsed {
		return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.ID), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	if !b.toolResultIsCancelled() && b.ResultContent != "" {
		// Keep the compact header summary to a single line; detailed multi-line
		// diagnostics are rendered below via renderLSPDiagnosticsLines.
		summary := strings.TrimSpace(b.ResultContent)
		if i := strings.IndexByte(summary, '\n'); i >= 0 {
			summary = strings.TrimSpace(summary[:i])
		}
		if summary != "" {
			result = append(result, "  "+DimStyle.Render(summary))
		}
	}

	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		rows, sourceSample := parsePlainContentPreviewLines(parsed.Content)
		if len(rows) > 0 {
			result = append(result, renderNumberedToolPreview(numberedToolPreviewOptions{
				filePath:            filePath,
				rows:                rows,
				sourceSample:        sourceSample,
				contentWidth:        contentWidth,
				codeWidth:           codeWidth,
				defaultVisibleLines: maxReadDefaultLines,
				expanded:            b.ReadContentExpanded,
				highlighter:         &b.codeHL,
			})...)
		}
	}

	if writeToolResultExtraVisible(b) {
		result = append(result, ToolResultExpandedStyle.Render("  ↳ Result:"))
		result = append(result, renderLSPDiagnosticsLines(b.ResultContent, "    ", cardWidth-4)...)
	}
	if b.toolResultIsError() && b.ResultContent != "" {
		result = append(result, ErrorStyle.Render("  ↳ Error:"))
		result = append(result, renderLSPDiagnosticsLines(b.ResultContent, "    ", cardWidth-4)...)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = append(result, DimStyle.Render("  ↳ Cancelled"))
		if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
			result = append(result, renderLSPDiagnosticsLines(detail, "    ", cardWidth-4)...)
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.ID), result, toolCardBg, railANSISeq("tool", b.Focused))
}
