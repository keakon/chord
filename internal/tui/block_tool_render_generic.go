package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

const bashCommandPreviewMaxLines = 2

// bashCollapsedResultMinVisibleLines is the total output line count below
// which the collapsed Bash card shows all output inline instead of folding
// behind an expand hint.  When total lines exceed this threshold only the
// first line is shown as a preview with an "N more lines" expand hint.
const bashCollapsedResultMinVisibleLines = 5

// bashCommandBlockLines returns the logical command lines to render in the
// body (full when expanded, preview when collapsed) and how many further
// lines remain hidden under the preview.
func bashCommandBlockLines(command string, expanded bool) (lines []string, hidden int) {
	if expanded {
		return bashCommandLines(command), 0
	}
	return bashCommandPreviewLines(command, bashCommandPreviewMaxLines)
}

// renderCommandBlock renders a `Title:` labelled, indented block of lines in
// the shared dim style used by Bash / Shell transcript cards. It is the single
// renderer for the command body across collapsed preview and expanded full
// views, as well as the local `!shell` card.
func renderCommandBlock(title string, lines []string, contentWidth int) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, DimStyle.Render("  "+title+":"))
	for _, line := range lines {
		for _, w := range wrapIndentedText(line, contentWidth) {
			out = append(out, DimStyle.Render("    "+w))
		}
	}
	return out
}

func appendBashCommandBlock(result *[]string, command string, contentWidth int, expanded bool, showExpandHint bool) {
	lines, hidden := bashCommandBlockLines(command, expanded)
	*result = append(*result, renderCommandBlock("Command", lines, contentWidth)...)
	if showExpandHint && hidden > 0 {
		*result = append(*result, renderToolExpandHint(toolHintIndent, hidden))
	}
}

// bashMetaLines returns the wrapped, styled meta lines (description, Workdir,
// Timeout) displayed in the expanded Bash card body.
func bashMetaLines(vals map[string]string, contentWidth int) []string {
	var out []string
	if desc := strings.TrimSpace(vals["description"]); desc != "" {
		line := fmt.Sprintf("description: %s", desc)
		for _, w := range wrapIndentedText(line, contentWidth) {
			out = append(out, DimStyle.Render("    "+w))
		}
	}
	if workdir := strings.TrimSpace(vals["workdir"]); workdir != "" {
		line := fmt.Sprintf("Workdir: %s", workdir)
		for _, w := range wrapIndentedText(line, contentWidth) {
			out = append(out, DimStyle.Render("  "+w))
		}
	}
	if timeout := strings.TrimSpace(vals["timeout"]); timeout != "" {
		line := fmt.Sprintf("Timeout: %ss", timeout)
		for _, w := range wrapIndentedText(line, contentWidth) {
			out = append(out, DimStyle.Render("  "+w))
		}
	}
	return out
}

func appendBashCollapsedSummary(result *[]string, b *Block, vals map[string]string, contentWidth int, includeDescription bool) {
	if b == nil || !b.ResultDone {
		return
	}
	stderr, stdout := bashSplitResultStreams(b)
	content := stdout
	if strings.TrimSpace(content) == "" {
		content = stderr
	}
	if strings.TrimSpace(content) == "" {
		return
	}
	isError := b.toolResultIsError()
	style := DimStyle
	if isError {
		style = ErrorStyle
	}

	total := toolCollapsedVisibleLineCount(content, contentWidth)
	if total <= bashCollapsedResultMinVisibleLines {
		// Short output: show all lines inline.
		for _, line := range toolExpandedTextLines(content, contentWidth) {
			*result = append(*result, style.Render("  "+line))
		}
		return
	}

	// Longer output: show single-line summary.
	line, lineIsError := bashCollapsedSummaryLine(b, vals, contentWidth, includeDescription)
	if line == "" {
		return
	}
	if lineIsError {
		style = ErrorStyle
	}
	for _, wrapped := range wrapIndentedText(line, contentWidth) {
		*result = append(*result, style.Render("  "+wrapped))
	}
}

func appendBashExpandedResult(result *[]string, b *Block, contentWidth int) {
	if b == nil {
		return
	}
	stderr, stdout := bashSplitResultStreams(b)
	exitLabel := bashExpandedExitLine(b)
	if exitLabel != "" {
		for _, wrapped := range wrapIndentedText(exitLabel, contentWidth) {
			*result = append(*result, DimStyle.Render("  "+wrapped))
		}
	}
	if stderr != "" {
		*result = append(*result, ErrorStyle.Render("  Stderr:"))
		for _, line := range toolExpandedTextLines(stderr, contentWidth) {
			*result = append(*result, ErrorStyle.Render("    "+line))
		}
	}
	if stdout != "" {
		*result = append(*result, ToolResultExpandedStyle.Render("  Stdout:"))
		for _, line := range toolExpandedTextLines(stdout, contentWidth) {
			*result = append(*result, ToolResultExpandedStyle.Render("    "+line))
		}
	}
}

// bashCollapsedResultIsShort returns true when the Bash output is short
// enough to display inline in a collapsed card without an expand hint.
func bashCollapsedResultIsShort(b *Block, contentWidth int) bool {
	if b == nil || contentWidth <= 0 || !b.ResultDone {
		return false
	}
	stderr, stdout := bashSplitResultStreams(b)
	content := stdout
	if strings.TrimSpace(content) == "" {
		content = stderr
	}
	if strings.TrimSpace(content) == "" {
		return true
	}
	return toolCollapsedVisibleLineCount(content, contentWidth) <= bashCollapsedResultMinVisibleLines
}

func bashCollapsedResultHiddenLines(b *Block, contentWidth int) int {
	if b == nil || contentWidth <= 0 {
		return 0
	}
	stderr, stdout := bashSplitResultStreams(b)
	content := stdout
	if strings.TrimSpace(content) == "" {
		content = stderr
	}
	if strings.TrimSpace(content) == "" {
		return 0
	}
	total := toolCollapsedVisibleLineCount(content, contentWidth)
	// When total output is short enough to show inline in the collapsed
	// card (see appendBashCollapsedSummary), there are no hidden lines.
	if total <= bashCollapsedResultMinVisibleLines {
		return 0
	}
	visible := 0
	if line := bashFirstNonEmptyLine(content); line != "" {
		visible = len(wrapText(line, contentWidth))
	}
	return total - visible
}

func bashCollapsedCommandHiddenLines(command string, contentWidth int) int {
	if contentWidth <= 0 {
		return 0
	}
	lines, hidden := bashCommandPreviewLines(command, bashCommandPreviewMaxLines)
	if hidden <= 0 {
		return 0
	}
	visible := 0
	for _, line := range lines {
		visible += len(wrapIndentedText(line, contentWidth))
	}
	total := 0
	for _, line := range bashCommandLines(command) {
		total += len(wrapIndentedText(line, contentWidth))
	}
	if total > visible {
		return total - visible
	}
	return 0
}

func (b *Block) renderToolCall(width int, spinnerFrame string) []string {
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

	if b.ToolName == "TodoWrite" {
		return b.renderTodoCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameWrite {
		return b.renderWriteCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameEdit {
		return b.renderFileDiffCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameRead {
		return b.renderReadCall(width, spinnerFrame)
	}
	if b.ToolName == "Question" {
		return b.renderQuestionCall(width, spinnerFrame)
	}
	if b.ToolName == "Delegate" {
		return b.renderTaskCall(width, spinnerFrame)
	}
	if b.ToolName == "Cancel" {
		return b.renderCancelCall(width, spinnerFrame)
	}
	if b.ToolName == "Notify" {
		return b.renderNotifyCall(width, spinnerFrame)
	}
	if toolUsesCompactDetailToggle(b.ToolName) {
		return b.renderCompactExpandableToolCall(width, spinnerFrame)
	}

	keys, vals := b.toolArgsParsed()
	paramSummary, mainPart, grayPart, _, _, _, _ := b.toolHeaderMeta()
	if paramSummary == "" {
		paramSummary = extractToolParamsWithParsed(keys, vals, width-20)
	} else if runewidth.StringWidth(paramSummary) > width-20 {
		paramSummary = runewidth.Truncate(paramSummary, width-20, "…")
	}
	if mainPart != "" || grayPart != "" {
		if maxMain := width - 20 - runewidth.StringWidth(grayPart) - 1; maxMain > 0 && runewidth.StringWidth(mainPart) > maxMain {
			mainPart = runewidth.Truncate(mainPart, maxMain, "…")
		}
	}
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""

	var result []string
	if b.Collapsed {
		prefix := b.renderToolPrefix(spinnerFrame)
		if isActive {
			toolName := ToolCallStyle.Render(b.ToolName)
			headerLine := "  " + prefix + " " + toolName
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + DimStyle.Render(grayPart)
				}
			} else if paramSummary != "" {
				headerLine += " " + DimStyle.Render(paramSummary)
			}
			headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
			result = append(result, headerLine)
		} else {
			headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName)
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + DimStyle.Render(grayPart)
				}
			} else if paramSummary != "" {
				headerLine += " " + DimStyle.Render(paramSummary)
			}
			headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
			result = append(result, ToolCallStyle.Render(headerLine))
		}

		if b.DoneSummary != "" {
			summary := truncateOneLine(b.DoneSummary, width-30)
			result = append(result, ToolResultStyle.Render(fmt.Sprintf("  ▸ ↳ ✓ %s", summary)))
		} else if b.ResultContent != "" {
			displayResult := toolCollapsedResultContent(b.ToolName, b.ResultContent)
			lineCount := len(strings.Split(displayResult, "\n"))
			summary := truncateOneLine(displayResult, width-30)
			if b.toolResultIsError() {
				result = append(result, ErrorStyle.Render(fmt.Sprintf("  ▸ ↳ %s (%d lines)", summary, lineCount)))
			} else if b.toolResultIsCancelled() {
				result = append(result, DimStyle.Render(fmt.Sprintf("  ▸ ↳ cancelled (%d lines)", lineCount)))
			} else {
				result = append(result, ToolResultStyle.Render(fmt.Sprintf("  ▸ ↳ %s (%d lines)", summary, lineCount)))
			}
		}
	} else {
		prefix := b.renderToolPrefix(spinnerFrame)
		showParamSummary := (mainPart != "" || grayPart != "" || paramSummary != "") && (paramSummary != "" || mainPart != "" || grayPart != "")
		if isActive {
			toolName := ToolCallStyle.Render(b.ToolName)
			headerLine := "  " + prefix + " " + toolName
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + DimStyle.Render(grayPart)
				}
			} else if showParamSummary && paramSummary != "" {
				headerLine += " " + DimStyle.Render(paramSummary)
			}
			headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
			result = append(result, headerLine)
		} else {
			headerLine := fmt.Sprintf("  %s %s", prefix, b.ToolName)
			if mainPart != "" || grayPart != "" {
				headerLine += " " + mainPart
				if grayPart != "" {
					headerLine += " " + DimStyle.Render(grayPart)
				}
			} else if showParamSummary && paramSummary != "" {
				headerLine += " " + DimStyle.Render(paramSummary)
			}
			headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
			styledHeader := ToolCallStyle.Render(headerLine)
			if b.toolExecutionIsQueued() {
				styledHeader = renderQueuedToolHeaderBadge(styledHeader, cardWidth)
			}
			result = append(result, styledHeader)
		}
		if paramSummary == "" || b.ToolName == "Bash" {
			_, _, _, _, _, _, paramLines := b.toolHeaderMeta()
			for _, line := range paramLines {
				for _, wrapped := range wrapText(line, contentWidth) {
					result = append(result, DimStyle.Render("    "+wrapped))
				}
			}
		}
		if summary := formatToolResultSummaryLine(b); summary != "" && !b.toolExecutionIsQueued() {
			result = append(result, toolSummaryLine(summary))
		}
		if b.ResultContent != "" {
			if len(result) > 0 {
				result = append(result, "")
			}
			if b.toolResultIsError() {
				result = append(result, ErrorStyle.Render("  ↳ Error:"))
			} else if b.toolResultIsCancelled() {
				result = appendCancelledResultLines(result, b.ResultContent, contentWidth)
			}
			lineStyle := DimStyle
			if b.ToolName == tools.NameDelete && !b.toolResultIsError() && !b.toolResultIsCancelled() {
				lineStyle = ToolResultExpandedStyle
			}
			displayResult := toolExpandedResultContent(b.ToolName, b.ResultContent)
			for _, line := range wrapText(displayResult, contentWidth) {
				result = append(result, lineStyle.Render("    "+line))
			}
		}
		if b.DoneSummary != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range wrapText(b.DoneSummary, contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func compactToolHiddenResultLines(b *Block, contentWidth int) int {
	if b == nil || !toolUsesCompactDetailToggle(b.ToolName) {
		return 0
	}
	if strings.TrimSpace(b.ResultContent) == "" || (b.toolResultIsCancelled() && toolCancelledDetailText(b.ResultContent) == "") {
		return 0
	}
	displayResult := toolExpandedResultContent(b.ToolName, b.ResultContent)
	_, hidden, _ := toolExpandedResultLines(displayResult, contentWidth, false, b.ResultDone && !b.toolResultIsError() && !b.toolResultIsCancelled())
	return hidden
}

func compactToolResultForceExpanded(toolName string, hidden int) bool {
	if hidden != 1 {
		return false
	}
	switch toolName {
	case "Delete", "Grep", "Glob":
		return true
	default:
		return false
	}
}

func (b *Block) compactToolResultForceExpanded(contentWidth int) bool {
	return compactToolResultForceExpanded(b.ToolName, compactToolHiddenResultLines(b, contentWidth))
}

func compactToolContentWidthForRenderWidth(width int) int {
	boxWidth := width - ToolBlockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - ToolBlockStyle.GetHorizontalPadding() - ToolBlockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	return contentWidth
}

func (b *Block) compactToolResultForceExpandedForRenderWidth(width int) bool {
	if width <= 0 {
		return false
	}
	return b.compactToolResultForceExpanded(compactToolContentWidthForRenderWidth(width))
}

func (b *Block) renderCompactExpandableToolCall(width int, spinnerFrame string) []string {
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
	contentWidth := compactToolContentWidthForRenderWidth(width)
	collapsedPreviewLine := ""

	expanded := b.ToolCallDetailExpanded || b.compactToolResultForceExpanded(contentWidth)
	keys, vals := b.toolArgsParsed()
	_, mainPart, grayPart, collapsedMain, collapsedGray, collapsedOK, _ := b.toolHeaderMeta()
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""
	if b.ToolName == "Bash" && !expanded && collapsedOK {
		mainPart, grayPart = collapsedMain, collapsedGray
	}

	result := make([]string, 0, 16)
	prefix := b.renderToolPrefixForExpanded(spinnerFrame, expanded)
	toolHeaderLine := ToolCallStyle.Render(fmt.Sprintf("  %s %s", prefix, b.ToolName))
	if isActive {
		toolHeaderLine = "  " + prefix + " " + ToolCallStyle.Render(b.ToolName)
	}
	if mainPart != "" || grayPart != "" {
		if maxMain := contentWidth - 20 - runewidth.StringWidth(grayPart) - 1; maxMain > 0 && runewidth.StringWidth(mainPart) > maxMain {
			mainPart = runewidth.Truncate(mainPart, maxMain, "…")
		}
		toolHeaderLine += " " + mainPart
		if grayPart != "" {
			toolHeaderLine += " " + DimStyle.Render(grayPart)
		}
	}
	toolHeaderLine = appendToolProgressSuffix(toolHeaderLine, b.ToolProgress, cardWidth-4)
	if b.toolExecutionIsQueued() && !isActive {
		toolHeaderLine = renderQueuedToolHeaderBadge(toolHeaderLine, cardWidth)
	}
	result = append(result, toolHeaderLine)

	if b.ToolName == "Bash" {
		shortResult := bashCollapsedResultIsShort(b, contentWidth)
		appendBashCommandBlock(&result, vals["command"], contentWidth, expanded || shortResult, expanded)
		if expanded {
			result = append(result, bashMetaLines(cloneToolValsWithDisplayDirs(b, vals), contentWidth)...)
		} else {
			appendBashCollapsedSummary(&result, b, vals, contentWidth, !collapsedOK)
		}
	} else if mainPart == "" && len(keys) > 0 && b.ToolName != "Skill" && !(b.ToolName == "Complete" && !expanded) {
		if expanded {
			for _, k := range keys {
				line := fmt.Sprintf("%s: %s", k, vals[k])
				for _, w := range wrapIndentedText(line, contentWidth) {
					result = append(result, DimStyle.Render("    "+w))
				}
			}
		} else {
			k0 := keys[0]
			line := fmt.Sprintf("%s: %s", k0, vals[k0])
			collapsedPreviewLine = line
			for _, w := range wrapIndentedText(line, contentWidth) {
				result = append(result, DimStyle.Render("    "+w))
			}
		}
	}

	if b.ResultContent != "" || b.DoneSummary != "" || b.toolExecutionIsQueued() {
		if summary := formatToolResultSummaryLine(b); summary != "" && !b.toolExecutionIsQueued() && !(b.ToolName == "Bash" && !expanded) {
			result = append(result, toolSummaryLine(summary))
		}
		if b.ToolName == "Bash" {
			if expanded {
				appendBashExpandedResult(&result, b, contentWidth)
			} else if !bashCollapsedResultIsShort(b, contentWidth) {
				if hidden := bashCollapsedCommandHiddenLines(vals["command"], contentWidth) + bashCollapsedResultHiddenLines(b, contentWidth); hidden > 0 {
					result = append(result, renderToolExpandHint(toolHintIndent, hidden))
				}
			}
		} else {
			if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
				result = append(result, ErrorStyle.Render("  ↳ Error:"))
			} else if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
				if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
					result = append(result, DimStyle.Render("  ↳ Cancelled:"))
				}
			}
			if strings.TrimSpace(b.ResultContent) != "" && !(b.toolResultIsCancelled() && toolCancelledDetailText(b.ResultContent) == "") {
				if !expanded && b.ToolName == "Skill" && !b.toolResultIsError() && !b.toolResultIsCancelled() {
					// collapsed Skill cards intentionally show only header summary
				} else if !expanded && b.ToolName == "Complete" && !b.toolResultIsError() && !b.toolResultIsCancelled() {
					appendCollapsedSummaryLines(&result, b.ResultContent, cardWidth-10, ToolResultStyle)
					if hidden := toolCollapsedVisibleLineCount(b.ResultContent, contentWidth) - 2; hidden > 0 {
						result = append(result, renderToolExpandHint(toolHintIndent, hidden))
					}
				} else {
					displayResult := toolExpandedResultContent(b.ToolName, b.ResultContent)
					lines, hidden, usedMD := toolExpandedResultLines(displayResult, contentWidth, expanded, b.ResultDone && !b.toolResultIsError() && !b.toolResultIsCancelled())
					if !expanded && collapsedPreviewLine != "" && compactToolPreviewDuplicatesResult(collapsedPreviewLine, lines) {
						lines = nil
					}
					lineStyle := DimStyle
					if b.toolResultIsError() {
						lineStyle = ErrorStyle
					}
					for _, line := range lines {
						if usedMD {
							result = append(result, "    "+line)
						} else {
							result = append(result, lineStyle.Render(toolResultIndent+line))
						}
					}
					if !expanded && hidden > 0 {
						result = append(result, renderToolExpandHint(toolHintIndent, hidden))
					}
				}
			}
		}
		if strings.TrimSpace(b.DoneSummary) != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range toolExpandedTextLines(b.DoneSummary, contentWidth) {
				result = append(result, "    "+line)
			}
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderToolPrefix returns a concise status indicator.
func (b *Block) renderToolPrefix(spinnerFrame string) string {
	return b.renderToolPrefixForExpanded(spinnerFrame, b.ToolCallDetailExpanded)
}

func (b *Block) renderToolPrefixForExpanded(spinnerFrame string, compactExpanded bool) string {
	if b.toolExecutionIsRunning() && spinnerFrame != "" {
		iconColor := NeonAccentColor(1800 * time.Millisecond)
		styled := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor)).Render(spinnerFrame)
		if strings.HasSuffix(styled, "\x1b[0m") {
			styled = styled[:len(styled)-len("\x1b[0m")] + "\x1b[39m"
		} else if strings.HasSuffix(styled, "\x1b[m") {
			styled = styled[:len(styled)-len("\x1b[m")] + "\x1b[39m"
		}
		return styled
	}
	if b.toolExecutionIsQueued() {
		return DimStyle.Render(queuedToolGlyph)
	}
	if b.ToolName == "Delegate" {
		if b.toolResultIsError() {
			if b.Collapsed {
				return "✗▸"
			}
			return "✗▾"
		}
		if b.toolResultIsCancelled() {
			if b.Collapsed {
				return "◌▸"
			}
			return "◌▾"
		}
		if b.ResultDone || strings.TrimSpace(b.ResultContent) != "" {
			if b.Collapsed {
				return "✓▸"
			}
			return "✓▾"
		}
		if b.Collapsed {
			return "▸"
		}
		return "▾"
	}
	if toolUsesCompactDetailToggle(b.ToolName) {
		if !b.ResultDone {
			if compactExpanded {
				return "▾"
			}
			return "▸"
		}
		if b.toolResultIsError() {
			if compactExpanded {
				return "✗▾"
			}
			return "✗▸"
		}
		if b.toolResultIsCancelled() {
			if compactExpanded {
				return "◌▾"
			}
			return "◌▸"
		}
		if compactExpanded {
			return "✓▾"
		}
		return "✓▸"
	}
	if b.ResultContent != "" {
		if b.toolResultIsError() {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			return "◌"
		}
		return "✓"
	}
	if b.Collapsed {
		return "▸"
	}
	return "▾"
}

func (b *Block) renderToolResult(width int) []string {
	contentLines := strings.Split(b.Content, "\n")
	lineCount := len(contentLines)
	style := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - style.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - style.GetHorizontalPadding() - style.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	contentWidth := cardWidth - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	if b.Collapsed {
		prefix := "✓▸"
		if b.IsError || b.toolResultIsError() {
			prefix = "✗▸"
		} else if b.toolResultIsCancelled() {
			prefix = "◌▸"
		}
		var body []string
		body = append(body, ToolCallStyle.Render(fmt.Sprintf("  %s %s", prefix, b.ToolName)))
		lim := min(lineCount, maxToolCallCompactResultLines)
		for i := range lim {
			for _, w := range wrapText(contentLines[i], contentWidth) {
				body = append(body, DimStyle.Render(toolResultIndent+w))
			}
		}
		if lineCount > maxToolCallCompactResultLines {
			more := lineCount - maxToolCallCompactResultLines
			body = append(body, renderToolExpandHint(toolHintIndent, more))
		}
		return renderPrewrappedToolCard(style, cardWidth, ToolLabelStyle.Render("TOOL RESULT"), body, toolCardBg, railANSISeq("tool", b.Focused))
	}
	renderHeader := func(s string) string { return ToolResultExpandedStyle.Render(s) }
	renderBody := func(s string) string { return s }
	if b.IsError || b.toolResultIsError() {
		renderHeader = func(s string) string { return ErrorStyle.Render(s) }
		renderBody = func(s string) string { return ErrorStyle.Render(s) }
	}
	headerPrefix := "  ↳ Result from"
	if b.toolResultIsCancelled() {
		headerPrefix = "  ↳ Cancelled"
	}
	header := renderHeader(fmt.Sprintf("%s %s:", headerPrefix, b.ToolName))
	result := []string{header}
	cardBgStylePre := lipgloss.NewStyle().Background(style.GetBackground())
	if !b.IsError && !b.toolResultIsError() && !b.toolResultIsCancelled() && toolResultLooksLikeMarkdown(b.Content) {
		mdLines := renderMarkdownContent(b.Content, contentWidth)
		mdLines = preserveCardBg(mdLines, toolCardBg)
		for _, line := range mdLines {
			result = append(result, padLineToDisplayWidthWithStyle(cardBgStylePre, "    "+line, cardWidth))
		}
	} else {
		for _, line := range wrapText(b.Content, contentWidth) {
			result = append(result, "    "+renderBody(line))
		}
	}
	return renderPrewrappedToolCard(style, cardWidth, ToolLabelStyle.Render("TOOL RESULT"), result, toolCardBg, railANSISeq("tool", b.Focused))
}
