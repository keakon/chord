package tui

import (
	"encoding/json"
	"fmt"
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

type writeResultSections struct {
	summary     string
	diagnostics string
}

func splitWriteResult(result string) writeResultSections {
	result = strings.ReplaceAll(result, "\r\n", "\n")
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

func writeOperationSummary(b *Block, fileContent string, sections writeResultSections) string {
	summary := sections.summary
	if sections.diagnostics == "" {
		summary = strings.TrimSpace(toolDisplayResultContent(b))
	}
	if sections.diagnostics == "" {
		lines := strings.Split(summary, "\n")
		if len(lines) == 1 && strings.HasPrefix(lines[0], "Successfully wrote ") {
			summary = ""
		}
	}
	if summary == "" {
		if rows, _ := parsePlainContentPreviewLines(fileContent); len(rows) > 0 {
			summary = fmt.Sprintf("%d lines written", len(rows))
		} else {
			summary = strings.TrimSpace(toolSuccessfulFileOpSummary(b))
		}
	}
	return summary
}

func appendWriteDiagnostics(result []string, diagnostics string, width int) []string {
	if strings.TrimSpace(diagnostics) == "" {
		return result
	}
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Diagnostics:"))
	return append(result, renderLSPDiagnosticsLines(diagnostics, "    ", width)...)
}

func writeDiagnosticsSummary(diagnostics string) string {
	if strings.TrimSpace(diagnostics) == "" {
		return ""
	}
	if count := countLSPDiagnosticLines(diagnostics); count > 0 {
		return fmt.Sprintf("%d diagnostics", count)
	}
	return "diagnostics"
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

	prefix := b.renderToolPrefix(spinnerFrame)
	if b.ResultDone && !b.toolResultIsError() && !b.toolResultIsCancelled() && fileContent != "" {
		prefix = renderToolDisclosurePrefix(prefix, !b.Collapsed)
	}
	var result []string
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if filePath != "" {
		headerLine += " " + DimStyle.Render(filePath)
	}
	if extraText := strings.Join(extras, ", "); extraText != "" {
		if budget := max(cardWidth-writeHeaderExtrasReservedWidth, writeHeaderExtrasMinWidth); runewidth.StringWidth(stripANSI(extraText)) > budget {
			extraText = truncateToolHeaderGray(extraText, budget)
		}
		headerLine += " " + DimStyle.Render(extraText)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)
	sections := writeResultSections{}
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		sections = splitWriteResult(b.ResultContent)
	}

	if b.Collapsed {
		if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			for _, line := range wrapText(sanitizeToolDisplayText(toolDisplayResultContent(b)), contentWidth) {
				result = append(result, ErrorStyle.Render("    "+line))
			}
		} else if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, DimStyle.Render("  ↳ Cancelled"))
			if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
				for _, line := range wrapText(sanitizeToolDisplayText(detail), contentWidth) {
					result = append(result, DimStyle.Render("    "+line))
				}
			}
		} else {
			summary := writeOperationSummary(b, fileContent, sections)
			if summary != "" {
				result = append(result, ToolResultStyle.Render("  ↳ "+summary))
			}
			if diagnosticSummary := writeDiagnosticsSummary(sections.diagnostics); diagnosticSummary != "" {
				result = append(result, ToolResultStyle.Render("  ↳ "+diagnosticSummary))
			}
		}
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		summary := writeOperationSummary(b, fileContent, sections)
		if summary != "" {
			result = append(result, "  "+DimStyle.Render(summary))
		}
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
		result = append(result, ErrorStyle.Render("  ↳ Error:"))
		result = append(result, renderLSPDiagnosticsLines(b.ResultContent, "    ", cardWidth-4)...)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = append(result, DimStyle.Render("  ↳ Cancelled"))
		if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
			result = append(result, renderLSPDiagnosticsLines(detail, "    ", cardWidth-4)...)
		}
	}
	result = appendToolElapsedFooter(result, b)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}
