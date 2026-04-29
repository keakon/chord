package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// maxReadDefaultLines is the number of lines shown by default for Read tool results.
// When there are more lines, user can press space to expand (ReadContentExpanded).
const maxReadDefaultLines = 10

// renderReadCall renders a Read tool call with syntax-highlighted file content.
func (b *Block) renderReadCall(width int, spinnerFrame string) []string {
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
	headerLine := fmt.Sprintf("  %s %s", prefix, ToolCallStyle.Render(b.ToolName))
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
	headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
	result = append(result, headerLine)

	if b.toolResultIsError() && b.ResultContent != "" {
		result = appendErrorResultLines(result, b.ResultContent, contentWidth)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
	} else if b.ResultContent != "" {
		rows, sourceSample := parseReadDisplayLines(b.ResultContent)
		codeLines := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.IsCode {
				codeLines = append(codeLines, row.Content)
			}
		}
		var highlightedCodeLines []string
		codeIndex := 0
		if len(codeLines) > 0 {
			highlightedCodeLines = highlightCodeLines(ensureCodeHighlighter(&b.diffHL, filePath, sourceSample), codeLines, "")
		}
		cap := maxTUIDiffLines
		if !b.ReadContentExpanded && len(rows) > maxReadDefaultLines {
			cap = maxReadDefaultLines
		}
		shown := 0
		for _, row := range rows {
			if shown >= cap {
				hidden := len(rows) - cap
				result = append(result, renderToolExpandHint(toolHintIndent, hidden))
				break
			}
			if row.IsCode {
				highlighted := row.Content
				if codeIndex < len(highlightedCodeLines) {
					highlighted = highlightedCodeLines[codeIndex]
				}
				codeIndex++
				highlighted = ansi.Truncate(highlighted, codeWidth, "…")
				result = append(result, "  "+DimStyle.Render(row.LineNo)+"  "+highlighted)
			} else {
				wrapped := ansi.Truncate(row.Content, contentWidth, "…")
				result = append(result, "  "+DimStyle.Render(wrapped))
			}
			shown++
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}
