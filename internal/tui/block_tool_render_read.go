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

	resultOffset := b.readResultOffset()
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
		case "limit":
			if v != "" && v != "0" {
				limitOpt = k + "=" + v
			}
		case "offset":
			// offset is a 1-based start line; 1 (and 0) mean the default first
			// line and add no information to the header.
			if v != "" && v != "0" && v != "1" {
				offsetOpt = k + "=" + v
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
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		if meta, ok := parseReadResultMeta(b.ResultContent); ok {
			readSummary = readResultSummary(meta, !b.Collapsed)
		}
	}
	hasDisclosure := b.readCardHasDisclosure(contentWidth)
	prefix := b.renderToolPrefix(spinnerFrame)
	if hasDisclosure {
		prefix = renderToolDisclosurePrefix(prefix, !b.Collapsed)
	}
	var result []string
	headerLine := renderReadHeaderLine(prefix, b.ToolName, filePath, strings.Join(opts, ", "), readSummary, cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)

	if kind := toolOutcomeKindOf(b); kind != toolOutcomeNone {
		appendToolOutcomeBody(&result, kind, toolDisplayResultContent(b), contentWidth, !b.Collapsed)
	} else if b.ResultContent != "" {
		if !b.Collapsed {
			// The model-facing offset is already a 1-based start line; when the
			// result lacks a READ_RESULT header (legacy/restored output) the
			// gutter starts there, and 0 (absent) clamps to line 1 inside
			// parseReadDisplayLines.
			rows, sourceSample := parseReadDisplayLines(b.stripResultNotes(b.ResultContent), resultOffset)
			result = append(result, renderNumberedToolPreview(numberedToolPreviewOptions{
				filePath:     filePath,
				rows:         rows,
				sourceSample: sourceSample,
				contentWidth: contentWidth,
				highlighter:  &b.codeHL,
			})...)
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func readResultSummary(meta readResultMeta, includeDetails bool) string {
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
	if includeDetails && meta.Truncated {
		truncLabel := "output truncated"
		switch meta.TruncatedKind {
		case tools.ReadTruncatedStale:
			truncLabel = "stale result"
		case tools.ReadTruncatedSuperseded:
			truncLabel = "superseded result"
		}
		parts = append(parts, truncLabel)
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

// readResultOffset extracts the model-facing start line from the call args so a
// legacy result with no READ_RESULT header still numbers from the offset the
// caller asked for.
func (b *Block) readResultOffset() int {
	var parsed struct {
		Offset int `json:"offset"`
	}
	if json.Unmarshal([]byte(b.Content), &parsed) == nil {
		return parsed.Offset
	}
	return 0
}

// readCardHasDisclosure reports whether toggling the read card changes the body
// it renders, so the marker and ToggleAtWidth stay on one predicate: a card with
// no expandable body shows no disclosure and cannot toggle, and every card whose
// body the toggle reveals carries one. Failures and cancellations share the
// bounded outcome envelope, where a body short enough to fit the collapsed row
// or budget renders identically either way and so is not expandable — see
// toolOutcomeFoldable.
func (b *Block) readCardHasDisclosure(contentWidth int) bool {
	if b == nil || !b.ResultDone {
		return false
	}
	if toolOutcomeKindOf(b) != toolOutcomeNone {
		return toolOutcomeFoldable(b, contentWidth)
	}
	if b.ResultContent == "" {
		return false
	}
	rows, _ := parseReadDisplayLines(b.stripResultNotes(b.ResultContent), b.readResultOffset())
	return len(rows) > 0
}
