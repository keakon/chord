package tui

import (
	"encoding/json"
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

	var resultOffset int
	var parsed struct {
		Path   string `json:"path"`
		Limit  int    `json:"limit"`
		Offset int    `json:"offset"`
	}
	if json.Unmarshal([]byte(b.Content), &parsed) == nil {
		resultOffset = parsed.Offset
	}

	// Schema-invalid args (e.g. "offset":"1.0") must still surface every
	// argument the caller actually passed, so derive the header tolerantly.
	keys, vals := parseToolArgs(b.Content)
	filePath := ""
	var opts []string
	for _, k := range keys {
		if k == "path" {
			// The header is width-clipped anyway; do not pre-truncate the path.
			filePath = b.displayToolPath(vals[k])
			continue
		}
		v := truncateToolParamValue(vals[k])
		switch k {
		case "limit", "offset":
			if v != "" && v != "0" {
				opts = append(opts, k+"="+v)
			}
		default:
			opts = append(opts, k+"="+v)
		}
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := renderReadHeaderLine(prefix, b.ToolName, filePath, strings.Join(opts, ", "), cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if b.ResultContent != "" {
		rows, sourceSample := parseReadDisplayLines(b.ResultContent, resultOffset+1)
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

func renderReadHeaderLine(prefix, toolName, filePath, optText string, maxWidth int) string {
	headerLine := renderToolHeaderLine(prefix, toolName)
	if optText != "" {
		optText = "(" + optText + ")"
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
