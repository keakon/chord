package tui

import (
	"encoding/json"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Width budget for the extra arguments appended to a Write header: the card
// width minus the room taken by the prefix, tool name and file path, floored
// so a narrow card still shows something.
const (
	writeHeaderExtrasReservedWidth = 24
	writeHeaderExtrasMinWidth      = 20
)

// renderWriteCall renders a Write tool call result with a syntax-highlighted
// preview of the written file content.
func (b *Block) renderWriteCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := numberedToolPreviewWidth(cardWidth)

	var fileContent string
	var parsed struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	argsWellTyped := json.Unmarshal([]byte(b.Content), &parsed) == nil
	if argsWellTyped {
		fileContent = parsed.Content
	}

	// Schema-invalid args must still surface what the caller actually passed,
	// so derive the visible path and any unexpected keys tolerantly.
	keys, vals := parseToolArgs(b.Content)
	filePath := ""
	var extras []string
	for _, k := range keys {
		switch {
		case k == "path":
			// The header is width-clipped anyway; do not pre-truncate the path.
			filePath = b.displayToolPath(vals[k])
			continue
		case k == "content" && argsWellTyped:
			// The content is already rendered by the preview below.
			continue
		}
		extras = append(extras, k+"="+truncateToolParamValue(vals[k]))
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if filePath != "" {
		headerLine += " " + DimStyle.Render(filePath)
	}
	if extraText := strings.Join(extras, ", "); extraText != "" {
		if budget := max(cardWidth-writeHeaderExtrasReservedWidth, writeHeaderExtrasMinWidth); runewidth.StringWidth(extraText) > budget {
			extraText = runewidth.Truncate(extraText, budget, "…")
		}
		headerLine += " " + DimStyle.Render(extraText)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if b.Collapsed {
		return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	if !b.toolResultIsCancelled() && b.ResultContent != "" {
		// Keep the compact header summary to a single line; detailed multi-line
		// diagnostics are rendered below via renderLSPDiagnosticsLines.
		summary := strings.TrimSpace(toolDisplayResultContent(b))
		if i := strings.IndexByte(summary, '\n'); i >= 0 {
			summary = strings.TrimSpace(summary[:i])
		}
		if summary != "" {
			result = append(result, "  "+DimStyle.Render(summary))
		}
	}

	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		rows, sourceSample := parsePlainContentPreviewLines(fileContent)
		if len(rows) > 0 {
			result = append(result, renderNumberedToolPreview(numberedToolPreviewOptions{
				filePath:            filePath,
				rows:                rows,
				sourceSample:        sourceSample,
				contentWidth:        contentWidth,
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
	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}
