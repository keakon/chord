package tui

import (
	"encoding/json"
	"fmt"
	"strings"
)

// maxReadDefaultLines is the number of lines shown by default for Read tool results.
// When there are more lines, user can press space to expand (ReadContentExpanded).
const maxReadDefaultLines = 10

// renderReadCall renders a Read tool call with syntax-highlighted file content.
func (b *Block) renderReadCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := numberedToolPreviewWidth(cardWidth)

	var filePath string
	var readArgs struct {
		Path   string `json:"path"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if json.Unmarshal([]byte(b.Content), &readArgs) == nil {
		filePath = b.displayToolPath(readArgs.Path)
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if filePath != "" {
		headerLine += " " + filePath
	}
	if readArgs.Limit > 0 || readArgs.Offset > 0 {
		var opts []string
		if readArgs.Limit > 0 {
			opts = append(opts, fmt.Sprintf("limit=%d", readArgs.Limit))
		}
		if readArgs.Offset > 0 {
			opts = append(opts, fmt.Sprintf("offset=%d", readArgs.Offset))
		}
		if len(opts) > 0 {
			headerLine += " " + DimStyle.Render("("+strings.Join(opts, ", ")+")")
		}
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if b.ResultContent != "" {
		rows, sourceSample := parseReadDisplayLines(b.ResultContent, readArgs.Offset+1)
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
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.ID), result, toolCardBg, railANSISeq("tool", b.Focused))
}
