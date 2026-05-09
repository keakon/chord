package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const numberedToolPreviewLineNumberWidth = 6

type numberedToolPreviewOptions struct {
	filePath            string
	rows                []readDisplayLine
	sourceSample        string
	contentWidth        int
	codeWidth           int
	defaultVisibleLines int
	expanded            bool
	highlighter         **codeHighlighter
}

func numberedToolPreviewWidths(cardWidth int) (contentWidth, codeWidth int) {
	contentWidth = cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	codeWidth = contentWidth - numberedToolPreviewLineNumberWidth - 2
	if codeWidth < 10 {
		codeWidth = 10
	}
	return contentWidth, codeWidth
}

func renderNumberedToolPreview(opts numberedToolPreviewOptions) []string {
	if len(opts.rows) == 0 {
		return nil
	}

	cap := maxTUIDiffLines
	if !opts.expanded && opts.defaultVisibleLines > 0 && len(opts.rows) > opts.defaultVisibleLines {
		cap = opts.defaultVisibleLines
	}
	if cap > len(opts.rows) {
		cap = len(opts.rows)
	}
	visibleRows := opts.rows[:cap]
	hidden := len(opts.rows) - cap

	codeLines := make([]string, 0, len(visibleRows))
	for _, row := range visibleRows {
		if row.IsCode {
			codeLines = append(codeLines, row.Content)
		}
	}
	var highlightedCodeLines []string
	codeIndex := 0
	if len(codeLines) > 0 && opts.highlighter != nil {
		highlightedCodeLines = highlightCodeLines(ensureCodeHighlighter(opts.highlighter, opts.filePath, opts.sourceSample), codeLines, "")
	}

	result := make([]string, 0, len(visibleRows)+1)
	for _, row := range visibleRows {
		if row.IsCode {
			highlighted := row.Content
			if codeIndex < len(highlightedCodeLines) {
				highlighted = highlightedCodeLines[codeIndex]
			}
			codeIndex++
			highlighted = ansi.Truncate(highlighted, opts.codeWidth, "…")
			result = append(result, "  "+DimStyle.Render(row.LineNo)+"  "+highlighted)
		} else {
			wrapped := ansi.Truncate(row.Content, opts.contentWidth, "…")
			result = append(result, "  "+DimStyle.Render(wrapped))
		}
	}
	if hidden > 0 {
		result = append(result, renderToolExpandHint(toolHintIndent, hidden))
	}
	return result
}

func parsePlainContentPreviewLines(content string) ([]readDisplayLine, string) {
	if content == "" {
		return nil, ""
	}
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	trimmed := strings.TrimSuffix(normalized, "\n")
	var lines []string
	if trimmed == "" {
		lines = []string{""}
	} else {
		lines = strings.Split(trimmed, "\n")
	}

	rows := make([]readDisplayLine, 0, len(lines))
	codeLines := make([]string, 0, len(lines))
	for i, line := range lines {
		content := sanitizeToolDisplayText(line)
		lineNo := fmt.Sprintf("%d", i+1)
		rows = append(rows, readDisplayLine{IsCode: true, LineNo: lineNo, Content: content})
		codeLines = append(codeLines, content)
	}
	return rows, strings.Join(codeLines, "\n")
}
