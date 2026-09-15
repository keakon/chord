package tui

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/keakon/chord/internal/tui/markdownutil"
)

// writeSuccessResultRe parses the canonical write result line
// "Successfully wrote 71 lines, 2113 bytes" so the header can carry the counts
// as header facts instead of a second "↳" body line.
var writeSuccessResultRe = regexp.MustCompile(`^Successfully wrote (\d+) (line|lines), (\d+) (byte|bytes)$`)

func writeSuccessCountSummary(s string) string {
	m := writeSuccessResultRe.FindStringSubmatch(strings.TrimSpace(s))
	if len(m) != 5 {
		return ""
	}
	return m[1] + " " + m[2] + " · " + m[3] + " " + m[4]
}

type writeResultSections struct {
	summary     string
	diagnostics string
}

func splitWriteResult(result string) writeResultSections {
	result = markdownutil.NormalizeNewlines(result)
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "Diagnostics:", "Diagnostics summary:":
			return writeResultSections{
				summary:     strings.TrimSpace(strings.Join(lines[:i], "\n")),
				diagnostics: strings.TrimSpace(strings.Join(lines[i+1:], "\n")),
			}
		}
		if strings.Contains(line, "LSP:") || strings.Contains(line, "LSP errors detected") || strings.Contains(line, "<diagnostics") || lspSeverityRe.MatchString(line) || lspDiagLineRe.MatchString(trimmed) {
			return writeResultSections{
				summary:     strings.TrimSpace(strings.Join(lines[:i], "\n")),
				diagnostics: strings.TrimSpace(strings.Join(lines[i:], "\n")),
			}
		}
	}
	return writeResultSections{summary: strings.TrimSpace(result)}
}

func appendWriteDiagnostics(result []string, diagnostics string, width int) []string {
	if strings.TrimSpace(diagnostics) == "" {
		return result
	}
	result = append(result, toolFieldSection(ToolResultExpandedStyle, "Diagnostics"))
	return append(result, renderLSPDiagnosticsLines(diagnostics, "    ", width)...)
}

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
	extras = append(extras, b.diagnosticHeaderOptions()...)

	// Write cards are always expanded, so the header carries the counts and no
	// disclosure marker is needed.
	prefix := b.renderToolPrefix(spinnerFrame)
	// The result summary has to be known before the header is built, because a
	// write that reports line/byte counts merges them into the header line.
	// Building the header once keeps one truncation policy per card: the path
	// and parameters were previously appended by hand and then re-budgeted by
	// appendSearchHeaderSummary only on the counted path, so the same long path
	// could be shortened two different ways depending on the result text.
	sections := writeResultSections{}
	headerSummary := ""
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		sections = splitWriteResult(b.stripResultNotes(b.ResultContent))
		headerSummary = writeSuccessCountSummary(sections.summary)
	}
	headerLine := appendSearchHeaderSummary(renderToolHeaderLine(prefix, b.ToolName), filePath, strings.Join(extras, ", "), headerSummary, cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result := []string{headerLine}

	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		rows, sourceSample := parsePlainContentPreviewLines(fileContent)
		if len(rows) > 0 {
			result = append(result, renderNumberedToolPreview(numberedToolPreviewOptions{
				filePath:     filePath,
				rows:         rows,
				sourceSample: sourceSample,
				contentWidth: contentWidth,
				highlighter:  &b.codeHL,
			})...)
		}
		result = appendWriteDiagnostics(result, sections.diagnostics, cardWidth-4)
	}

	if b.toolResultIsError() && b.ResultContent != "" {
		// The diagnostics renderer keeps LSP paths aligned, so this card
		// formats its own body under the shared "↳ Error:" header.
		result = append(result, toolFieldSection(ErrorStyle, "Error"))
		result = append(result, renderLSPDiagnosticsLines(toolErrorDisplayContent(b.stripResultNotes(b.ResultContent)), "    ", cardWidth-4)...)
	} else if b.toolResultIsCancelled() {
		appendToolOutcomeBody(&result, toolOutcomeCancelled, toolDisplayResultContent(b), cardWidth-4, true)
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}
