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
	b.ToolName = tools.NormalizeName(b.ToolName)
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	if b.ToolName == tools.NameTodoWrite {
		return b.renderTodoCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameWrite {
		return b.renderWriteCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameEdit || b.ToolName == tools.NamePatch {
		return b.renderFileDiffCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameRead {
		return b.renderReadCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameQuestion {
		return b.renderQuestionCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameDelegate {
		return b.renderTaskCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameDone {
		return b.renderDoneCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameCancel {
		return b.renderCancelCall(width, spinnerFrame)
	}
	if b.ToolName == tools.NameNotify {
		return b.renderNotifyCall(width, spinnerFrame)
	}
	if toolUsesCompactDetailToggle(b.ToolName) {
		return b.renderCompactExpandableToolCall(width, spinnerFrame)
	}

	keys, vals := b.toolArgsParsed()
	paramSummary, mainPart, grayPart, _, _, _, _ := b.toolHeaderMeta()
	if mainPart != "" || grayPart != "" {
		metrics = newWideHeaderToolCardMetrics(width)
		blockStyle = metrics.blockStyle
		toolCardBg = metrics.toolCardBg
		cardWidth = metrics.cardWidth
		contentWidth = metrics.contentWidth
	}
	if paramSummary == "" {
		paramSummary = extractToolParamsWithParsed(keys, vals, cardWidth-16)
	} else if runewidth.StringWidth(paramSummary) > cardWidth-16 {
		paramSummary = runewidth.Truncate(paramSummary, cardWidth-16, "…")
	}
	var result []string
	if b.Collapsed {
		prefix := b.renderToolPrefix(spinnerFrame)
		headerLine := renderToolHeaderLine(prefix, b.ToolName)
		headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary, cardWidth-4)
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		result = append(result, headerLine)

		if b.DoneSummary != "" {
			summary := truncateOneLine(sanitizeToolDisplayText(b.DoneSummary), cardWidth-26)
			result = append(result, ToolResultStyle.Render(fmt.Sprintf("  ▸ ↳ ✓ %s", summary)))
		} else if b.ResultContent != "" {
			displayContent := toolDisplayResultContent(b)
			if b.toolResultIsError() {
				displayContent = toolErrorDisplayContent(displayContent)
			}
			displayResult := sanitizeToolDisplayText(toolCollapsedResultContent(b.ToolName, displayContent))
			lineCount := len(strings.Split(displayResult, "\n"))
			summary := truncateOneLine(displayResult, cardWidth-26)
			if b.toolResultIsError() {
				result = append(result, ErrorStyle.Render(fmt.Sprintf("  ▸ ↳ Error: %s (%d lines)", summary, lineCount)))
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
		if showParamSummary {
			headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary, cardWidth-4)
		}
		headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
		if b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent {
			headerLine = renderQueuedToolHeaderBadge(headerLine, cardWidth)
		}
		result = append(result, headerLine)
		if paramSummary == "" || b.ToolName == tools.NameShell {
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
			displayContent := toolDisplayResultContent(b)
			if b.toolResultIsError() {
				displayContent = toolErrorDisplayContent(displayContent)
			}
			displayResult := sanitizeToolDisplayText(displayContent)
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

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func (b *Block) renderDoneCall(width int, spinnerFrame string) []string {
	metrics := newDoneToolCardMetrics(width)
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
		if b.toolResultIsError() {
			statusText = toolErrorDisplayContent(statusText)
		}
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
	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
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
	if b.ToolName == tools.NameLsp && !b.toolResultIsError() && !b.toolResultIsCancelled() {
		displayResult = toolDisplayResultContent(b)
	}
	if b.ToolName == tools.NameDelete {
		displayResult = sanitizeToolDisplayText(displayResult)
		nonEmpty := 0
		for line := range strings.SplitSeq(strings.TrimRight(displayResult, "\n"), "\n") {
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
	if expanded || mainPart != "" || len(keys) == 0 || toolName == tools.NameSkill {
		return 0
	}
	// In compact mode we show only the first param line; Complete shows no param
	// lines until expanded.
	start := 1
	if toolName == tools.NameComplete {
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
	// Collapsed short Shell output already shows all stdout/stderr. Expanded
	// view adds any command lines hidden by the collapsed preview, meta lines,
	// and exit/stdout/stderr framing.
	hidden := bashCollapsedCommandHiddenLines(vals["command"], contentWidth)
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
		case tools.NameShell:
			if shellCollapsedResultIsShort(b, contentWidth) {
				hidden += bashCollapsedShortDetailHiddenLines(b, vals, contentWidth)
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		case tools.NameSkill:
			if !b.toolResultIsError() && !b.toolResultIsCancelled() {
				displayResult := sanitizeToolDisplayText(toolDisplayResultContent(b))
				hidden += toolCollapsedVisibleLineCount(displayResult, contentWidth)
			} else {
				hidden += compactToolHiddenResultLines(b, contentWidth)
			}
		case tools.NameComplete:
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
	metrics := newWideHeaderToolCardMetrics(width)
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
	if b.ToolName == tools.NameShell && !expanded && collapsedOK {
		mainPart, grayPart = collapsedMain, collapsedGray
	}

	result := make([]string, 0, 16)
	prefix := b.renderToolPrefixForExpanded(spinnerFrame, expanded)
	toolHeaderLine := renderToolHeaderLine(prefix, b.ToolName)
	toolHeaderLine = appendToolHeaderSummary(toolHeaderLine, mainPart, grayPart, "", cardWidth-4)
	toolHeaderLine = buildToolHeaderLine(toolHeaderLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, isActive)
	result = append(result, toolHeaderLine)

	if b.ToolName == tools.NameShell {
		appendBashCommandBlock(&result, vals["command"], contentWidth, expanded, expanded)
		if !expanded && shellCollapsedResultIsShort(b, contentWidth) && hiddenDetail > 0 {
			result = append(result, renderToolExpandHint(toolHintIndent, hiddenDetail))
			expandHintAdded = true
		}
		if expanded {
			result = append(result, bashMetaLines(cloneToolValsWithDisplayDirs(b, vals), contentWidth)...)
		} else {
			appendBashCollapsedSummary(&result, b, vals, contentWidth, !collapsedOK)
		}
	} else if mainPart == "" && len(keys) > 0 && b.ToolName != tools.NameSkill && !(b.ToolName == tools.NameComplete && !expanded) {
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
		if summary := formatToolResultSummaryLine(b); summary != "" && !b.toolExecutionIsQueued() && !(b.ToolName == tools.NameShell && !expanded) {
			result = append(result, toolSummaryLine(summary))
		}
		if b.ToolName == tools.NameShell {
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
				if !expanded && tools.NormalizeName(b.ToolName) == tools.NameSkill && !b.toolResultIsError() && !b.toolResultIsCancelled() {
					// collapsed Skill cards intentionally show only header summary
				} else if !expanded && tools.NormalizeName(b.ToolName) == tools.NameComplete && !b.toolResultIsError() && !b.toolResultIsCancelled() {
					appendCollapsedSummaryLines(&result, b.ResultContent, cardWidth-10, ToolResultStyle)
					if hidden := toolCollapsedVisibleLineCount(b.ResultContent, contentWidth) - 2; hidden > 0 {
						result = append(result, renderToolExpandHint(toolHintIndent, hidden))
						expandHintAdded = true
					}
				} else {
					displayResult := sanitizeToolDisplayText(toolDisplayResultContent(b))
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

	if len(b.ImageParts) > 0 {
		imagesRendered := b.appendImagePreviewLines(&result, contentWidth, toolCardBg, blockStyle.GetPaddingTop(), len(result) > 0)
		if !imagesRendered {
			label := "  📎"
			if len(b.ImageParts) > 1 {
				label = fmt.Sprintf("  📎 %d", len(b.ImageParts))
			}
			result = append(result, DimStyle.Render(label))
		}
	}

	// If the expanded view would reveal additional lines (params or result),
	// show a hint even when the collapsed rendering path hid all such content.
	if !expanded && !expandHintAdded && hiddenDetail > 0 {
		result = append(result, renderToolExpandHint(toolHintIndent, hiddenDetail))
		expandHintAdded = true
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
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
		return pendingToolGlyph
	}
	if b.ToolName == tools.NameDelegate {
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
		if b.ToolName == tools.NameDone && doneResultIsRejected(b.ResultContent) {
			return "✗"
		}
		if b.toolResultIsCancelled() {
			if b.ToolName == tools.NameDone {
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

func appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary string, maxWidth int) string {
	baseWidth := runewidth.StringWidth(stripANSI(headerLine))
	if baseWidth >= maxWidth {
		return runewidth.Truncate(headerLine, maxWidth, "…")
	}
	budget := maxWidth - baseWidth - 1
	if budget <= 0 {
		return headerLine
	}

	mainPart = sanitizeToolDisplayText(mainPart)
	grayPart = sanitizeToolDisplayText(grayPart)
	paramSummary = sanitizeToolDisplayText(paramSummary)
	if mainPart == "" && grayPart == "" {
		if paramSummary == "" {
			return headerLine
		}
		return headerLine + " " + DimStyle.Render(truncateToolHeaderTail(paramSummary, budget))
	}
	if mainPart == "" {
		return headerLine + " " + DimStyle.Render(truncateToolHeaderMiddle(grayPart, budget))
	}

	mainWidth := runewidth.StringWidth(mainPart)
	grayWidth := runewidth.StringWidth(grayPart)
	if grayPart == "" || mainWidth >= budget {
		return headerLine + " " + truncateToolHeaderTail(mainPart, budget)
	}

	remaining := budget - mainWidth - 1
	if remaining < 5 {
		return headerLine + " " + mainPart
	}
	if grayWidth > remaining {
		grayPart = truncateToolHeaderMiddle(grayPart, remaining)
	}
	return headerLine + " " + mainPart + " " + DimStyle.Render(grayPart)
}

func truncateToolHeaderTail(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	return runewidth.Truncate(s, maxWidth, "…")
}

func truncateToolHeaderMiddle(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 1 {
		return "…"
	}
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		if maxWidth <= 3 {
			return truncateToolHeaderTail(s, maxWidth)
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "("), ")")
		return "(" + truncateToolHeaderMiddle(inner, maxWidth-2) + ")"
	}
	if maxWidth <= 3 {
		return truncateToolHeaderTail(s, maxWidth)
	}
	leftWidth := (maxWidth - 1) / 2
	rightWidth := maxWidth - 1 - leftWidth
	return tuiCut(s, 0, leftWidth) + "…" + tuiCutRight(s, rightWidth)
}

func tuiCutRight(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	runes := []rune(s)
	width := 0
	for i := len(runes) - 1; i >= 0; i-- {
		w := runewidth.RuneWidth(runes[i])
		if width+w > maxWidth {
			return string(runes[i+1:])
		}
		width += w
	}
	return s
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
		b.appendImagePreviewLines(&body, contentWidth, toolCardBg, style.GetPaddingTop(), len(body) > 0)
		return renderPrewrappedToolCard(style, cardWidth, toolCardTitle("TOOL RESULT", b.displayLabelID()), body, toolCardBg, railANSISeq("tool", b.Focused))
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
	b.appendImagePreviewLines(&result, contentWidth, toolCardBg, style.GetPaddingTop(), len(result) > 0)
	return renderPrewrappedToolCard(style, cardWidth, toolCardTitle("TOOL RESULT", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}
