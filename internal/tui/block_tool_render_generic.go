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
// which the collapsed Shell card shows all output inline instead of folding
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
// the shared dim style used by Shell transcript cards. It is the single
// renderer for the command body across collapsed preview and expanded full
// views, as well as the local `!shell` card.
func renderCommandBlock(title string, lines []string, contentWidth int) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, DimStyle.Render("  "+title+":"))
	for _, line := range lines {
		line = sanitizeToolDisplayText(line)
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
// Timeout) displayed in the expanded Shell card body.
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

	total, truncated := toolPlainTextWrappedLineCount(content, contentWidth, bashCollapsedResultMinVisibleLines)
	if !truncated && total <= bashCollapsedResultMinVisibleLines {
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

// shellCollapsedResultIsShort returns true when the Shell output is short
// enough to display inline in a collapsed card without an expand hint.
func shellCollapsedResultIsShort(b *Block, contentWidth int) bool {
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
	count, truncated := toolPlainTextWrappedLineCount(content, contentWidth, bashCollapsedResultMinVisibleLines)
	return !truncated && count <= bashCollapsedResultMinVisibleLines
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
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	visible := 0
	if line := bashFirstNonEmptyLine(content); line != "" {
		visible = len(wrapText(line, contentWidth))
	}
	shortLimit := bashCollapsedResultMinVisibleLines
	if visible > shortLimit {
		shortLimit = visible
	}
	total, truncated := toolPlainTextWrappedLineCount(content, contentWidth, shortLimit)
	if !truncated && total <= bashCollapsedResultMinVisibleLines {
		return 0
	}
	if len(lines) > 1 {
		return len(lines) - 1
	}
	if total > visible {
		return total - visible
	}
	return 1
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
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

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
	if b.ToolName == "Done" {
		return b.renderDoneCall(width, spinnerFrame)
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
	var result []string
	if b.Collapsed {
		prefix := b.renderToolPrefix(spinnerFrame)
		headerLine := renderToolHeaderLine(prefix, b.ToolName)
		if mainPart != "" || grayPart != "" {
			headerLine += " " + sanitizeToolDisplayText(mainPart)
			if grayPart != "" {
				headerLine += " " + DimStyle.Render(sanitizeToolDisplayText(grayPart))
			}
		} else if paramSummary != "" {
			headerLine += " " + DimStyle.Render(sanitizeToolDisplayText(paramSummary))
		}
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)

		if b.DoneSummary != "" {
			summary := truncateOneLine(sanitizeToolDisplayText(b.DoneSummary), width-30)
			result = append(result, ToolResultStyle.Render(fmt.Sprintf("  ▸ ↳ ✓ %s", summary)))
		} else if b.ResultContent != "" {
			displayResult := sanitizeToolDisplayText(toolCollapsedResultContent(b.ToolName, b.ResultContent))
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
		headerLine := renderToolHeaderLine(prefix, b.ToolName)
		if mainPart != "" || grayPart != "" {
			headerLine += " " + sanitizeToolDisplayText(mainPart)
			if grayPart != "" {
				headerLine += " " + DimStyle.Render(sanitizeToolDisplayText(grayPart))
			}
		} else if showParamSummary && paramSummary != "" {
			headerLine += " " + DimStyle.Render(sanitizeToolDisplayText(paramSummary))
		}
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		if b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent {
			headerLine = renderQueuedToolHeaderBadge(headerLine, cardWidth)
		}
		result = append(result, headerLine)
		if paramSummary == "" || b.ToolName == "Shell" {
			_, _, _, _, _, _, paramLines := b.toolHeaderMeta()
			for _, line := range paramLines {
				for _, wrapped := range wrapText(sanitizeToolDisplayText(line), contentWidth) {
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
			displayResult := sanitizeToolDisplayText(toolExpandedResultContent(b.ToolName, b.ResultContent))
			for _, line := range wrapText(displayResult, contentWidth) {
				result = append(result, lineStyle.Render("    "+line))
			}
		}
		if b.DoneSummary != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range wrapText(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
	}

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func (b *Block) renderDoneCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	prefix := b.renderToolPrefix(spinnerFrame)
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	report := strings.TrimSpace(b.DoneReport)
	if report != "" {
		result = append(result, "")
		for _, line := range renderRichMarkdownContent(report, contentWidth, &b.richMarkdownHL) {
			result = append(result, "    "+line)
		}
	}
	if b.ResultDone && strings.TrimSpace(b.ResultContent) != "" {
		statusText := strings.TrimSpace(b.ResultContent)
		if doneResultIsRejected(statusText) {
			statusText = doneRejectedReason(statusText)
			result = append(result, "")
			for i, line := range wrapText(sanitizeToolDisplayText(statusText), contentWidth-len("  ↳ rejected reason: ")) {
				if i == 0 {
					result = append(result, ErrorStyle.Render("  ↳ rejected reason: "+line))
				} else {
					result = append(result, ErrorStyle.Render("    "+line))
				}
			}
		} else if report == "" {
			result = append(result, "")
			label := ToolResultExpandedStyle.Render("  ↳ Status:")
			if b.toolResultIsError() {
				label = ErrorStyle.Render("  ↳ Error:")
			} else if b.toolResultIsCancelled() {
				label = DimStyle.Render("  ↳ Cancelled:")
			}
			result = append(result, label)
			style := DimStyle
			if b.toolResultIsError() {
				style = ErrorStyle
			}
			for _, line := range wrapText(sanitizeToolDisplayText(statusText), contentWidth) {
				result = append(result, style.Render("    "+line))
			}
		}
	}
	result = appendToolElapsedFooter(result, b)
	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func doneResultIsRejected(result string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(result))
	return strings.HasPrefix(trimmed, "done rejected:") || strings.HasPrefix(trimmed, "done rejected automatically:")
}

func doneRejectedReason(result string) string {
	trimmed := strings.TrimSpace(result)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "done rejected automatically:") {
		return strings.TrimSpace(trimmed[len("Done rejected automatically:"):])
	}
	if strings.HasPrefix(lower, "done rejected:") {
		return strings.TrimSpace(trimmed[len("Done rejected:"):])
	}
	return trimmed
}

func compactToolHiddenResultLines(b *Block, contentWidth int) int {
	if b == nil || !toolUsesCompactDetailToggle(b.ToolName) {
		return 0
	}
	if strings.TrimSpace(b.ResultContent) == "" || (b.toolResultIsCancelled() && toolCancelledDetailText(b.ResultContent) == "") {
		return 0
	}
	displayResult := toolExpandedResultContent(b.ToolName, b.ResultContent)
	if b.ToolName == "Delete" {
		displayResult = sanitizeToolDisplayText(displayResult)
		nonEmpty := 0
		for _, line := range strings.Split(strings.TrimRight(displayResult, "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				nonEmpty++
			}
		}
		if nonEmpty > 1 {
			return 1
		}
		return 0
	}
	resLines := strings.Split(strings.TrimRight(displayResult, "\n"), "\n")
	if len(resLines) > maxToolCallCompactResultLines+1 {
		return len(resLines) - maxToolCallCompactResultLines
	}
	_, hidden := toolExpandedResultLines(displayResult, contentWidth, false)
	return hidden
}

func compactToolHiddenParamLines(toolName string, keys []string, vals map[string]string, mainPart string, contentWidth int, expanded bool) int {
	if expanded || mainPart != "" || len(keys) == 0 || toolName == "Skill" {
		return 0
	}
	// In compact mode we show only the first param line; Complete shows no param
	// lines until expanded.
	start := 1
	if toolName == "Complete" {
		start = 0
	}
	if start >= len(keys) {
		return 0
	}
	hidden := 0
	for _, k := range keys[start:] {
		line := fmt.Sprintf("%s: %s", k, vals[k])
		hidden += len(wrapIndentedText(line, contentWidth))
	}
	return hidden
}

func bashCollapsedShortDetailHiddenLines(b *Block, vals map[string]string, contentWidth int) int {
	if b == nil || contentWidth <= 0 || !b.ResultDone {
		return 0
	}
	// Collapsed short Shell output already shows all stdout/stderr and (often)
	// the full command. Expanded view adds meta lines and an exit/stdout/stderr
	// framing.
	hidden := 0
	meta := bashMetaLines(cloneToolValsWithDisplayDirs(b, vals), contentWidth)
	hidden += len(meta)
	if exitLabel := bashExpandedExitLine(b); exitLabel != "" {
		hidden += len(wrapIndentedText(exitLabel, contentWidth))
	}
	stderr, stdout := bashSplitResultStreams(b)
	if stderr != "" {
		hidden++ // "Stderr:" heading
	}
	if stdout != "" {
		hidden++ // "Stdout:" heading
	}
	return hidden
}

func compactToolHiddenDetailLines(b *Block, keys []string, vals map[string]string, mainPart string, contentWidth int, expanded bool) int {
	if b == nil || contentWidth <= 0 || expanded || !toolUsesCompactDetailToggle(b.ToolName) {
		return 0
	}
	hidden := 0
	hidden += compactToolHiddenParamLines(b.ToolName, keys, vals, mainPart, contentWidth, expanded)

	// Result differences.
	if strings.TrimSpace(b.ResultContent) != "" && !(b.toolResultIsCancelled() && toolCancelledDetailText(b.ResultContent) == "") {
		switch b.ToolName {
		case "Shell":
			if shellCollapsedResultIsShort(b, contentWidth) {
				hidden += bashCollapsedShortDetailHiddenLines(b, vals, contentWidth)
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		case "Skill":
			if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				displayResult := sanitizeToolDisplayText(toolExpandedResultContent(b.ToolName, b.ResultContent))
				hidden += toolCollapsedVisibleLineCount(displayResult, contentWidth)
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		case "Complete":
			if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				if more := toolCollapsedVisibleLineCount(b.ResultContent, contentWidth) - 2; more > 0 {
					hidden += more
				}
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		default:
			hidden += compactToolHiddenResultLines(b, contentWidth)
		}
	}
	return hidden
}

func (b *Block) compactToolResultForceExpanded(contentWidth int) bool {
	if b == nil {
		return false
	}
	keys, vals := b.toolArgsParsed()
	_, mainPart, _, _, _, _, _ := b.toolHeaderMeta()
	hidden := compactToolHiddenDetailLines(b, keys, vals, mainPart, contentWidth, false)
	return hidden == 1
}

func compactToolContentWidthForRenderWidth(width int) int {
	return newToolCardMetrics(width).contentWidth
}

func (b *Block) compactToolResultForceExpandedForRenderWidth(width int) bool {
	if width <= 0 {
		return false
	}
	return b.compactToolResultForceExpanded(compactToolContentWidthForRenderWidth(width))
}

func (b *Block) renderCompactExpandableToolCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := compactToolContentWidthForRenderWidth(width)
	collapsedPreviewLine := ""
	expandHintAdded := false

	expanded := b.ToolCallDetailExpanded || b.compactToolResultForceExpanded(contentWidth)
	keys, vals := b.toolArgsParsed()
	_, mainPart, grayPart, collapsedMain, collapsedGray, collapsedOK, _ := b.toolHeaderMeta()
	hiddenDetail := 0
	if !expanded {
		hiddenDetail = compactToolHiddenDetailLines(b, keys, vals, mainPart, contentWidth, false)
	}
	isActive := b.toolExecutionIsRunning() && spinnerFrame != ""
	if b.ToolName == "Shell" && !expanded && collapsedOK {
		mainPart, grayPart = collapsedMain, collapsedGray
	}

	result := make([]string, 0, 16)
	prefix := b.renderToolPrefixForExpanded(spinnerFrame, expanded)
	toolHeaderLine := renderToolHeaderLine(prefix, b.ToolName)
	if mainPart != "" || grayPart != "" {
		if maxMain := contentWidth - 20 - runewidth.StringWidth(grayPart) - 1; maxMain > 0 && runewidth.StringWidth(mainPart) > maxMain {
			mainPart = runewidth.Truncate(mainPart, maxMain, "…")
		}
		toolHeaderLine += " " + sanitizeToolDisplayText(mainPart)
		if grayPart != "" {
			toolHeaderLine += " " + DimStyle.Render(sanitizeToolDisplayText(grayPart))
		}
	}
	toolHeaderLine = buildToolHeaderLine(toolHeaderLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, isActive)
	result = append(result, toolHeaderLine)

	if b.ToolName == "Shell" {
		shortResult := shellCollapsedResultIsShort(b, contentWidth)
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
		if summary := formatToolResultSummaryLine(b); summary != "" && !b.toolExecutionIsQueued() && !(b.ToolName == "Shell" && !expanded) {
			result = append(result, toolSummaryLine(summary))
		}
		if b.ToolName == "Shell" {
			if expanded {
				appendBashExpandedResult(&result, b, contentWidth)
			} else if !shellCollapsedResultIsShort(b, contentWidth) {
				if hidden := bashCollapsedCommandHiddenLines(vals["command"], contentWidth) + bashCollapsedResultHiddenLines(b, contentWidth); hidden > 0 {
					result = append(result, renderToolExpandHint(toolHintIndent, hidden))
					expandHintAdded = true
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
						expandHintAdded = true
					}
				} else {
					displayResult := sanitizeToolDisplayText(toolExpandedResultContent(b.ToolName, b.ResultContent))
					lines, hidden := toolExpandedResultLines(displayResult, contentWidth, expanded)
					if !expanded && collapsedPreviewLine != "" && compactToolPreviewDuplicatesResult(collapsedPreviewLine, lines) {
						lines = nil
					}
					lineStyle := DimStyle
					if b.toolResultIsError() {
						lineStyle = ErrorStyle
					}
					for _, line := range lines {
						result = append(result, lineStyle.Render(toolResultIndent+line))
					}
					if !expanded && hidden > 0 {
						result = append(result, renderToolExpandHint(toolHintIndent, hidden))
						expandHintAdded = true
					}
				}
			}
		}
		if strings.TrimSpace(b.DoneSummary) != "" {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Completed:"))
			for _, line := range toolExpandedTextLines(sanitizeToolDisplayText(b.DoneSummary), contentWidth) {
				result = append(result, "    "+line)
			}
		}
	}

	// If the expanded view would reveal additional lines (params or result),
	// show a hint even when the collapsed rendering path hid all such content.
	if !expanded && !expandHintAdded && hiddenDetail > 0 {
		result = append(result, renderToolExpandHint(toolHintIndent, hiddenDetail))
		expandHintAdded = true
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}

// renderToolPrefix returns a concise status indicator.
func (b *Block) renderToolPrefix(spinnerFrame string) string {
	return b.renderToolPrefixForExpanded(spinnerFrame, b.ToolCallDetailExpanded)
}

func styleToolStatusPrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	if strings.HasPrefix(prefix, "✓") {
		return ToolStatusSuccessStyle.Render("✓") + prefix[len("✓"):]
	}
	if strings.HasPrefix(prefix, "✗") {
		return ToolStatusErrorStyle.Render("✗") + prefix[len("✗"):]
	}
	for _, marker := range []string{"◌", queuedToolGlyph, pendingToolGlyph} {
		if strings.HasPrefix(prefix, marker) {
			return ToolStatusNeutralStyle.Render(marker) + prefix[len(marker):]
		}
	}
	return prefix
}

func renderToolHeaderLine(prefix, toolName string) string {
	return "  " + styleToolStatusPrefix(prefix) + " " + ToolCallStyle.Render(toolName)
}

func renderAnimatedToolPrefixGlyph(spinnerFrame string) string {
	iconColor := NeonAccentColor(1800 * time.Millisecond)
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor)).Render(spinnerFrame)
	if strings.HasSuffix(styled, "\x1b[0m") {
		return styled[:len(styled)-len("\x1b[0m")] + "\x1b[39m"
	}
	if strings.HasSuffix(styled, "\x1b[m") {
		return styled[:len(styled)-len("\x1b[m")] + "\x1b[39m"
	}
	return styled
}

func (b *Block) renderToolPrefixForExpanded(spinnerFrame string, compactExpanded bool) string {
	if b.toolExecutionIsRunning() && spinnerFrame != "" {
		return renderAnimatedToolPrefixGlyph(spinnerFrame)
	}
	if b.toolExecutionIsQueued() {
		if b.ToolQueuedByExecutionEvent {
			return queuedToolGlyph
		}
		if spinnerFrame != "" {
			return renderAnimatedToolPrefixGlyph(spinnerFrame)
		}
		return pendingToolGlyph
	}
	if b.ToolName == "Delegate" {
		if b.toolResultIsError() {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			return "◌"
		}
		if b.ResultDone || strings.TrimSpace(b.ResultContent) != "" {
			return "✓"
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
			return "✗"
		}
		if b.ToolName == "Done" && doneResultIsRejected(b.ResultContent) {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			if b.ToolName == "Done" {
				return "✗"
			}
			return "◌"
		}
		return "✓"
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
	metrics := newToolCardMetrics(width)
	style := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth
	if b.Collapsed {
		prefix := "✓"
		if b.IsError || b.toolResultIsError() {
			prefix = "✗"
		} else if b.toolResultIsCancelled() {
			prefix = "◌"
		}
		var body []string
		body = append(body, renderToolHeaderLine(prefix, b.ToolName))
		lim := min(lineCount, maxToolCallCompactResultLines)
		for i := range lim {
			for _, w := range wrapText(sanitizeToolDisplayText(contentLines[i]), contentWidth) {
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
	for _, line := range wrapText(sanitizeToolDisplayText(b.Content), contentWidth) {
		result = append(result, "    "+renderBody(line))
	}
	return renderPrewrappedToolCard(style, cardWidth, ToolLabelStyle.Render("TOOL RESULT"), result, toolCardBg, railANSISeq("tool", b.Focused))
}
