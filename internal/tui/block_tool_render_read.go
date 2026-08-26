package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

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
	var limitOpt, offsetOpt string
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
				if k == "offset" {
					offsetOpt = k + "=" + v
				} else {
					limitOpt = k + "=" + v
				}
			}
		default:
			opts = append(opts, k+"="+v)
		}
	}
	if offsetOpt != "" {
		opts = append(opts, offsetOpt)
	}
	if limitOpt != "" {
		opts = append(opts, limitOpt)
	}
	opts = append(opts, b.diagnosticHeaderOptions()...)

	readSummary := ""
	hasDisclosure := false
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		if meta, ok := parseReadResultMeta(b.ResultContent); ok {
			readSummary = readResultSummary(meta)
			hasDisclosure = meta.StartLine > 0 && meta.EndLine >= meta.StartLine
		}
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	if b.ResultDone && hasDisclosure {
		prefix = renderToolDisclosurePrefix(prefix, !b.Collapsed)
	}
	var result []string
	headerLine := renderReadHeaderLine(prefix, b.ToolName, filePath, strings.Join(opts, ", "), readSummary, cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if b.ResultContent != "" {
		if !b.Collapsed {
			rows, sourceSample := parseReadDisplayLines(b.ResultContent, resultOffset+1)
			result = append(result, renderNumberedToolPreview(numberedToolPreviewOptions{
				filePath:     filePath,
				rows:         rows,
				sourceSample: sourceSample,
				contentWidth: contentWidth,
				highlighter:  &b.codeHL,
			})...)
		}
	}
	result = appendToolElapsedFooter(result, b)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func readResultSummary(meta readResultMeta) string {
	parts := make([]string, 0, 3)
	if meta.RangeField != "" {
		parts = append(parts, "lines "+meta.RangeField+" of "+strconv.Itoa(meta.Total))
	} else if meta.StartLine > 0 && meta.EndLine >= meta.StartLine && meta.Total > 0 {
		parts = append(parts, fmt.Sprintf("lines %d–%d of %d", meta.StartLine, meta.EndLine, meta.Total))
	} else if meta.Total == 0 {
		parts = append(parts, "empty file")
	} else {
		parts = append(parts, fmt.Sprintf("no lines · %d total", meta.Total))
	}
	if meta.Truncated {
		truncLabel := "output truncated"
		switch meta.TruncatedKind {
		case tools.ReadTruncatedStale:
			truncLabel = "stale result"
		case tools.ReadTruncatedSuperseded:
			truncLabel = "superseded result"
		}
		parts = append(parts, truncLabel)
	}
	if meta.ArtifactPath != "" {
		parts = append(parts, "full output saved to "+meta.ArtifactPath)
	}
	return strings.Join(parts, " · ")
}

func renderReadHeaderLine(prefix, toolName, filePath, optText, resultSummary string, maxWidth int) string {
	headerLine := renderToolHeaderLine(prefix, toolName)
	if optText != "" {
		optText = "(" + optText + ")"
	}
	suffix := optText
	if resultSummary != "" {
		if suffix != "" {
			suffix += " · "
		}
		suffix += resultSummary
	}
	if filePath == "" && suffix == "" {
		return truncateToolHeaderTail(headerLine, maxWidth)
	}

	baseWidth := runewidth.StringWidth(stripANSI(headerLine))
	budget := maxWidth - baseWidth - 1
	if budget <= 0 {
		return truncateToolHeaderTail(headerLine, maxWidth)
	}
	if filePath == "" {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderGray(suffix, budget))
	}
	if suffix == "" {
		return headerLine + " " + truncateToolHeaderMiddle(filePath, budget)
	}

	optWidth := runewidth.StringWidth(stripANSI(suffix))
	if optWidth+1 >= budget {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderGray(suffix, budget))
	}
	pathBudget := budget - optWidth - 1
	return headerLine + " " + truncateToolHeaderMiddle(filePath, pathBudget) + " " + DimStyle.Render(suffix)
}
