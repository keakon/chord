package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// maxTUIDiffLines is the maximum number of diff lines rendered in the TUI.
const maxTUIDiffLines = 200

// renderWriteCall renders a Write tool call as code with line numbers and syntax highlighting.
func (b *Block) renderWriteCall(width int, spinnerFrame string) []string {
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	const lineNumWidth = 6
	codeWidth := contentWidth - lineNumWidth - 2
	if codeWidth < 10 {
		codeWidth = 10
	}

	var filePath string
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(b.Content), &parsed) == nil {
		filePath = b.displayToolPath(parsed.Path)
	}

	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := fmt.Sprintf("  %s %s", prefix, ToolCallStyle.Render(b.ToolName))
	if filePath != "" {
		headerLine += " " + DimStyle.Render(filePath)
	}
	headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
	result = append(result, headerLine)

	if b.Collapsed {
		return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	contentLines := writeDiffToContentLines(b.Diff)
	if !b.toolResultIsCancelled() {
		highlightedLines := highlightCodeLines(ensureCodeHighlighter(&b.diffHL, filePath, strings.Join(contentLines, "\n")), contentLines, "")
		cap := maxTUIDiffLines
		shown := 0
		for i := range contentLines {
			if shown >= cap {
				result = append(result, DimStyle.Render(fmt.Sprintf("  ... (%d lines hidden)", len(contentLines)-cap)))
				break
			}
			highlighted := highlightedLines[i]
			highlighted = ansi.Truncate(highlighted, codeWidth, "…")
			lineNum := fmt.Sprintf("%*d", lineNumWidth, shown+1)
			result = append(result, "  "+DimStyle.Render(lineNum)+"  "+highlighted)
			shown++
		}
		if len(contentLines) == 0 {
			result = append(result, "  "+DimStyle.Render("(empty file)"))
		}
	}

	if writeEditToolResultExtraVisible(b) {
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

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}
