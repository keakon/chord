package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// maxReadDefaultLines is the number of lines shown by default for Read tool results.
// When there are more lines, user can press space to expand (ReadContentExpanded).
const maxReadDefaultLines = 10

// renderReadCall renders a Read tool call with syntax-highlighted file content.
func (b *Block) renderReadCall(width int, spinnerFrame string) []string {
	metrics := newWideHeaderToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

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
	headerLine := renderReadHeaderLine(prefix, b.ToolName, filePath, readArgs.Limit, readArgs.Offset, cardWidth-4)
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

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func renderReadHeaderLine(prefix, toolName, filePath string, limit, offset, maxWidth int) string {
	headerLine := renderToolHeaderLine(prefix, toolName)
	var opts []string
	if limit > 0 {
		opts = append(opts, fmt.Sprintf("limit=%d", limit))
	}
	if offset > 0 {
		opts = append(opts, fmt.Sprintf("offset=%d", offset))
	}
	optText := ""
	if len(opts) > 0 {
		optText = "(" + strings.Join(opts, ", ") + ")"
	}
	if filePath == "" && optText == "" {
		return truncateToolHeaderTail(headerLine, maxWidth)
	}

	baseWidth := runewidth.StringWidth(stripANSI(headerLine))
	budget := maxWidth - baseWidth - 1
	if budget <= 0 {
		return truncateToolHeaderTail(headerLine, maxWidth)
	}
	if filePath == "" {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderMiddle(optText, budget))
	}
	if optText == "" {
		return headerLine + " " + truncateToolHeaderMiddle(filePath, budget)
	}

	optWidth := runewidth.StringWidth(optText)
	if optWidth+1 >= budget {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderMiddle(optText, budget))
	}
	pathBudget := budget - optWidth - 1
	return headerLine + " " + truncateToolHeaderMiddle(filePath, pathBudget) + " " + DimStyle.Render(optText)
}
