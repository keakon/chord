package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/tools"
)

var (
	// diffHunkHeaderRe parses unified diff hunk header: @@ -oldStart,oldCount +newStart,newCount @@
	diffHunkHeaderRe = regexp.MustCompile(`^@@ -(\d+),(\d+) \+(\d+),(\d+) @@`)

	// diffAddBg / diffDelBg: subtle line backgrounds (opencode-style) so syntax highlighting stays readable.
	diffAddBg = currentTheme.DiffAddLineBg
	diffDelBg = currentTheme.DiffDelLineBg

	lspDiagLineRe = regexp.MustCompile(`^\s*\d+:\d+`)

	// lspSeverityRe matches "[E]", "[W]", "[I]", "[H]" prefixes in LSP diagnostic output.
	lspSeverityRe = regexp.MustCompile(`^\s*\[(E|W|I|H)\]`)

	lspDiagnosticsOmittedLineRe = regexp.MustCompile(`^\s*\.\.\. \d+ diagnostics not shown due to output limits; they may still need fixing\.$`)

	// readResultLineRe matches any READ_RESULT metadata line (including the
	// lines=none empty-range form) so it is never rendered as a source line.
	readResultLineRe = regexp.MustCompile(`^READ_RESULT\b.*\blines=(?:\d+-\d+|none)\b`)
	// readResultRangeRe extracts the 1-based start line when a concrete range
	// is present; lines=none carries no start line.
	readResultRangeRe = regexp.MustCompile(`\blines=(\d+)-(\d+)\b`)
)

// maxToolCallCompactResultLines is the default visible height for generic tool output until space expands.
const maxToolCallCompactResultLines = 10

const (
	toolResultIndent = "    "
	toolHintIndent   = "    "
)

var activeToolSpinnerSegments = [...]string{"▖", "▘", "▝", "▗"}

const queuedToolGlyph = "⏸"

type toolCardMetrics struct {
	blockStyle   lipgloss.Style
	toolCardBg   string
	cardWidth    int
	contentWidth int
}

func newToolCardMetrics(width int) toolCardMetrics {
	return newToolCardMetricsWithContentCap(width, maxTextWidth)
}

func newWideHeaderToolCardMetrics(width int) toolCardMetrics {
	return newToolCardMetricsForHeaderWidth(width, maxTextWidth, true)
}

func newToolCardMetricsWithContentCap(width, contentCap int) toolCardMetrics {
	return newToolCardMetricsForHeaderWidth(width, contentCap, false)
}

func newToolCardMetricsForHeaderWidth(width, contentCap int, wideHeader bool) toolCardMetrics {
	blockStyle := ToolBlockStyle
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	if !wideHeader {
		// Keep the card surface aligned with the prose cards' right edge on very
		// wide viewports instead of stretching empty background past the text cap.
		cardWidth = clampCardInnerWidth(cardWidth, blockStyle, contentCap)
	}
	contentWidth := min(cardWidth-4, contentCap)
	if contentWidth < 10 {
		contentWidth = 10
	}
	return toolCardMetrics{
		blockStyle:   blockStyle,
		toolCardBg:   currentTheme.ToolCallBg,
		cardWidth:    cardWidth,
		contentWidth: contentWidth,
	}
}

func newDoneToolCardMetrics(width int) toolCardMetrics {
	return newToolCardMetricsWithContentCap(width, maxProseWidth)
}

// pendingToolGlyph is used for speculative tool cards that have finished
// streaming their arguments but have not yet transitioned into an explicit
// execution-state (running/queued) event.
//
// Keep this a single-column glyph (runewidth=1) to avoid header layout drift.
const pendingToolGlyph = "⧗"

func toolUsesCompactDetailToggle(toolName string) bool {
	switch toolName {
	case tools.NameWrite, tools.NameEdit, tools.NamePatch, tools.NameRead, tools.NameTodoWrite, tools.NameQuestion, tools.NameDelegate:
		return false
	}
	return true
}

func toolCollapsedSummaryText(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	trimmed = strings.ReplaceAll(trimmed, "\r", "\n")
	lines := strings.Split(trimmed, "\n")
	parts := make([]string, 0, 2)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts = append(parts, line)
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func appendCollapsedSummaryLines(result *[]string, summary string, width int, style lipgloss.Style) {
	trimmed := strings.TrimSpace(sanitizeToolDisplayText(summary))
	if trimmed == "" {
		return
	}
	oneLine := truncateOneLine(toolCollapsedSummaryText(trimmed), width)
	if oneLine == "" {
		return
	}
	*result = append(*result, style.Render("  ▸ ↳ "+oneLine))
}

func toolExpandedTextLines(s string, width int) []string {
	trimmed := strings.TrimSpace(sanitizeToolDisplayText(s))
	if trimmed == "" {
		return nil
	}
	return wrapText(trimmed, width)
}

func normalizedCompactToolPreviewText(s string) string {
	trimmed := strings.TrimSpace(sanitizeToolDisplayText(s))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\r\n", "\n")
	trimmed = strings.ReplaceAll(trimmed, "\r", "\n")
	for strings.Contains(trimmed, "\n\n") {
		trimmed = strings.ReplaceAll(trimmed, "\n\n", "\n")
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

func toolCardTitle(label string, id int) string {
	return ToolLabelStyle.Render(blockLabelWithID(label, id))
}

func compactToolPreviewDuplicatesResult(previewLine string, resultLines []string) bool {
	previewNorm := normalizedCompactToolPreviewText(previewLine)
	if previewNorm == "" || len(resultLines) == 0 {
		return false
	}
	previewValueNorm := ""
	if _, after, ok := strings.Cut(previewLine, ":"); ok {
		previewValueNorm = normalizedCompactToolPreviewText(after)
	}
	for _, line := range resultLines {
		lineNorm := normalizedCompactToolPreviewText(line)
		if lineNorm == "" {
			continue
		}
		return lineNorm == previewNorm || (previewValueNorm != "" && lineNorm == previewValueNorm)
	}
	return false
}

func toolCollapsedVisibleLineCount(s string, width int) int {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0
	}
	return len(wrapText(trimmed, width))
}

func toolPlainTextWrappedLineCount(text string, width int, limit int) (count int, truncated bool) {
	if width <= 0 {
		width = 80
	}
	if text == "" {
		return 0, false
	}
	for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		lineCount := len(wrapText(line, width))
		if lineCount == 0 {
			lineCount = 1
		}
		count += lineCount
		if limit > 0 && count > limit {
			return count, true
		}
	}
	return count, false
}

func toolExpandedResultLines(displayResult string, width int, expanded bool) ([]string, int) {
	trimmed := strings.TrimSpace(displayResult)
	if trimmed == "" {
		return nil, 0
	}
	resLines := strings.Split(strings.TrimRight(displayResult, "\n"), "\n")
	n := len(resLines)
	lim := n
	if !expanded && n > maxToolCallCompactResultLines {
		lim = maxToolCallCompactResultLines
	}
	out := make([]string, 0, lim)
	for i := 0; i < lim; i++ {
		out = append(out, wrapText(resLines[i], width)...)
	}
	hidden := 0
	if !expanded {
		visible := len(out)
		total, truncated := toolPlainTextWrappedLineCount(displayResult, width, visible+1)
		if truncated && n > lim {
			// Avoid wrapping the entire hidden tail in collapsed mode. This is a
			// cheap logical-line suffix, not an exact wrapped-line count.
			hidden = n - lim
		} else if total > visible {
			hidden = total - visible
		}
	}
	return out, hidden
}

func toolSummaryLine(line string) string {
	if line == "" {
		return ""
	}
	return ToolResultExpandedStyle.Render("  ↳ " + line)
}

func toolCancelledDetailText(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return ""
	}
	switch strings.ToLower(trimmed) {
	case "cancelled", "canceled":
		return ""
	default:
		return trimmed
	}
}

func appendCancelledResultLines(result []string, content string, width int) []string {
	result = append(result, DimStyle.Render("  ↳ Cancelled"))
	if detail := toolCancelledDetailText(content); detail != "" {
		for _, line := range wrapText(sanitizeToolDisplayText(detail), width) {
			result = append(result, DimStyle.Render("    "+line))
		}
	}
	return result
}

func appendToolElapsedFooter(result []string, b *Block) []string {
	if b == nil || !b.ResultDone {
		return result
	}
	if elapsed := b.toolElapsedLabel(); elapsed != "" {
		result = append(result, "")
		result = append(result, "  "+DimStyle.Render(fmt.Sprintf("⏱ %s", elapsed)))
	}
	return result
}

func appendErrorResultLines(result []string, content string, width int) []string {
	trimmed := strings.TrimSpace(sanitizeToolDisplayText(content))
	if trimmed == "" {
		return result
	}
	result = append(result, ErrorStyle.Render("  ↳ Error:"))
	for _, line := range wrapText(trimmed, width) {
		result = append(result, ErrorStyle.Render("    "+line))
	}
	return result
}

func bashCollapsedSummaryLine(b *Block, vals map[string]string, width int, includeDescription bool) (string, bool) {
	if b == nil || !b.ResultDone {
		return "", false
	}
	parts := make([]string, 0, 2)
	if includeDescription {
		if desc := strings.TrimSpace(vals["description"]); desc != "" {
			parts = append(parts, desc)
		}
	}
	summary, isError := bashCollapsedOutcomeSummary(b)
	if summary != "" {
		parts = append(parts, summary)
	}
	if len(parts) == 0 {
		return "", isError
	}
	line := strings.Join(parts, " · ")
	if width > 0 && runewidth.StringWidth(line) > width {
		return runewidth.Truncate(line, width, "…"), isError
	}
	return line, isError
}

func bashCollapsedOutcomeSummary(b *Block) (string, bool) {
	if b == nil || !b.ResultDone {
		return "", false
	}
	if b.toolResultIsCancelled() {
		return "cancelled", false
	}
	if b.toolResultIsError() {
		if timedOut := sanitizeToolDisplayText(bashTimeoutSummary(b.ResultContent)); timedOut != "" {
			return timedOut, true
		}
		if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(bashErrorBody(b.ResultContent))); line != "" {
			return truncateOneLine(line, 120), true
		}
		if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(b.ResultContent)); line != "" {
			return truncateOneLine(line, 120), true
		}
		if exit := sanitizeToolDisplayText(bashExitCodeFromError(b.ResultContent)); exit != "" {
			return exit, true
		}
		return "failed", true
	}
	_, stdout := bashSplitResultStreams(b)
	if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(stdout)); line != "" {
		return truncateOneLine(line, 120), false
	}
	if line := bashFirstNonEmptyLine(sanitizeToolDisplayText(strings.TrimSpace(b.ResultContent))); line != "" {
		return truncateOneLine(line, 120), false
	}
	return "completed", false
}

func bashExpandedExitLine(b *Block) string {
	if b == nil || !b.ResultDone {
		return ""
	}
	if b.toolResultIsCancelled() {
		return ""
	}
	if b.toolResultIsError() {
		if timedOut := bashTimeoutSummary(b.ResultContent); timedOut != "" {
			return "Exit: timeout"
		}
		if exit := bashExitCodeFromError(b.ResultContent); exit != "" {
			return "Exit: " + strings.TrimSpace(exit)
		}
		return "Exit: error"
	}
	return "Exit: 0"
}

func bashSplitResultStreams(b *Block) (stderr, stdout string) {
	if b == nil {
		return "", ""
	}
	trimmed := strings.TrimSpace(b.ResultContent)
	if trimmed == "" {
		return "", ""
	}
	if b.toolResultIsCancelled() {
		return "", ""
	}
	if b.toolResultIsError() {
		body := strings.TrimSpace(bashErrorBody(trimmed))
		if body == "" {
			body = trimmed
		}
		return body, ""
	}
	return "", trimmed
}

func bashErrorBody(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	const marker = "after output:"
	_, after, ok := strings.Cut(trimmed, marker)
	if !ok {
		return trimmed
	}
	body := strings.TrimSpace(after)
	return body
}

func bashTimeoutSummary(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "timed out after") {
		prefix := strings.TrimPrefix(trimmed, "command ")
		if before, _, ok := strings.Cut(prefix, " after output:"); ok {
			return before
		}
		if before, _, ok := strings.Cut(prefix, "\n"); ok {
			return before
		}
		return prefix
	}
	return ""
}

func bashExitCodeFromError(content string) string {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "exit code ") {
		return ""
	}
	line := trimmed
	if i := strings.Index(line, "\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(strings.TrimPrefix(line, "exit code "))
}

func bashFirstNonEmptyLine(content string) string {
	for line := range strings.SplitSeq(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func formatToolResultSummaryLine(b *Block) string {
	if b == nil {
		return ""
	}
	b.ToolName = tools.NormalizeName(b.ToolName)
	if b.toolResultIsCancelled() {
		return "Cancelled"
	}
	if !b.ResultDone {
		return ""
	}
	trimmed := strings.TrimSpace(b.ResultContent)
	switch b.ToolName {
	case tools.NameShell:
		// Shell expands with explicit exit-code detail, so avoid a redundant summary like "Passed".
		if b.toolResultIsError() {
			return ""
		}
		if trimmed == "" {
			return ""
		}
		return ""
	case tools.NameSpawn:
		if b.toolResultIsError() {
			return "Failed"
		}
		return "Started"
	case tools.NameSpawnStop:
		if b.toolResultIsError() {
			return "Failed"
		}
		return "Stopped"
	case tools.NameDelegate:
		if b.DoneSummary != "" {
			return "Done"
		}
		if b.toolResultIsError() {
			return "Error"
		}
		if id := parseTaskResultInstanceID(trimmed); id != "" {
			return fmt.Sprintf("Spawned · %s", id)
		}
		if trimmed != "" {
			return "Spawned"
		}
		return "Running"
	case tools.NameGrep:
		if b.toolResultIsError() {
			return "Search failed"
		}
		if trimmed == "No matches found." {
			return ""
		}
		count := 0
		for line := range strings.SplitSeq(strings.TrimRight(trimmed, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "(showing first ") {
				continue
			}
			count++
		}
		if count <= 1 {
			return ""
		}
		return fmt.Sprintf("%d matches", count)
	case tools.NameGlob:
		if b.toolResultIsError() {
			return "Search failed"
		}
		if trimmed == "No files matched the pattern." {
			return ""
		}
		count := 0
		for line := range strings.SplitSeq(strings.TrimRight(trimmed, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "(showing first ") {
				continue
			}
			count++
		}
		if count <= 1 {
			return ""
		}
		return fmt.Sprintf("%d files", count)
	case tools.NameCancel:
		if b.toolResultIsError() {
			return "Failed"
		}
		handle, ok := parseTaskToolHandle(trimmed)
		if ok && handle.Status != "" {
			switch handle.Status {
			case "stopped":
				return "Stopped"
			case "cancelled":
				return "Cancelled"
			default:
				return "Stopped"
			}
		}
		return "Stopped"
	case tools.NameNotify:
		if b.toolResultIsError() {
			return "Failed"
		}
		handle, ok := parseTaskToolHandle(trimmed)
		if ok && handle.Status != "" {
			switch handle.Status {
			case "delivered":
				return "Delivered"
			case "queued":
				return "Queued"
			case "rehydrated":
				return "Rehydrated"
			default:
				return "Sent"
			}
		}
		if strings.TrimSpace(trimmed) != "" {
			return "Sent"
		}
		return "Sent"
	default:
		if b.Audit != nil && b.Audit.UserModified {
			return "edited before approval"
		}
		if !b.toolResultIsError() {
			if summary := toolSuccessfulFileOpSummary(b); summary != "" {
				return summary
			}
		}
		return ""
	}
}

func renderQueuedToolHeaderBadge(line string, width int) string {
	const badgeText = "Queued"
	const rightPadding = 1
	trimmed := strings.TrimRight(line, " ")
	badge := DimStyle.Render(badgeText)
	badgeWidth := runewidth.StringWidth(badgeText)
	lineWidth := ansi.StringWidth(trimmed)
	if lineWidth == 0 {
		return badge + strings.Repeat(" ", rightPadding)
	}
	if width <= 0 {
		width = lineWidth + 1 + badgeWidth + rightPadding
	}
	availableGap := width - lineWidth - badgeWidth - rightPadding
	if availableGap < 2 {
		return trimmed
	}
	return trimmed + strings.Repeat(" ", availableGap) + badge + strings.Repeat(" ", rightPadding)
}

func renderToolExpandHint(indent string, hidden int) string {
	if hidden <= 0 {
		return ""
	}
	return DimStyle.Render(fmt.Sprintf("%s── %d more lines · [space] toggle expand/collapse ──", indent, hidden))
}

func ensureCodeHighlighter(slot **codeHighlighter, filePath, sample string) *codeHighlighter {
	return ensureCodeHighlighterWithLanguage(slot, filePath, sample, "")
}

func ensureCodeHighlighterWithLanguage(slot **codeHighlighter, filePath, sample, language string) *codeHighlighter {
	if *slot == nil {
		*slot = newCodeHighlighterWithLanguage(filePath, sample, language)
		return *slot
	}
	(*slot).updateContext(filePath, sample)
	(*slot).updateLanguage(language)
	return *slot
}

func highlightCodeLines(h *codeHighlighter, lines []string, bgTerm string) []string {
	if len(lines) == 0 {
		return nil
	}
	source := strings.Join(lines, "\n")
	if source != "" {
		source += "\n"
	}
	highlighted := h.highlightSnippet(source, bgTerm)
	highlighted = strings.TrimSuffix(highlighted, "\n")
	highlightedLines := strings.Split(highlighted, "\n")
	if len(highlightedLines) == len(lines) {
		return highlightedLines
	}

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, h.highlightLine(line, bgTerm))
	}
	return out
}

func sanitizeToolDisplayText(s string) string {
	return sanitizeDisplayText(s)
}

func toolErrorDisplayContent(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "Error: ") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "Error: "))
	}
	if strings.HasPrefix(trimmed, "Error:\n") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "Error:"))
	}
	return content
}

func parseReadDisplayLines(result string, startLine int) ([]readDisplayLine, string) {
	if result == "" {
		return nil, ""
	}
	if startLine < 1 {
		startLine = 1
	}
	rawLines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	rows := make([]readDisplayLine, 0, len(rawLines))
	codeLines := make([]string, 0, len(rawLines))
	sourceLineNo := startLine

	for i, line := range rawLines {
		line = strings.TrimSuffix(line, "\r")
		content := sanitizeToolDisplayText(line)
		if i == 0 {
			// The READ_RESULT metadata line is for the model only; never render
			// it in the card. Use its 1-based start line to align code numbering
			// (it is authoritative when the model's offset and the returned range
			// differ, e.g. after a budget truncation).
			if readResultLineRe.MatchString(content) {
				if m := readResultRangeRe.FindStringSubmatch(content); len(m) == 3 {
					if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
						sourceLineNo = n
					}
				}
				continue
			}
		}
		rows = append(rows, readDisplayLine{IsCode: true, LineNo: fmt.Sprintf("%d", sourceLineNo), Content: content})
		codeLines = append(codeLines, content)
		sourceLineNo++
	}

	return rows, strings.Join(codeLines, "\n")
}

func diffContentSample(diff string) string {
	const maxSampleLines = 64
	if diff == "" {
		return ""
	}
	rawLines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	sample := make([]string, 0, min(len(rawLines), maxSampleLines))
	for _, line := range rawLines {
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "@@"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
			continue
		case strings.HasPrefix(line, " "), strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			sample = append(sample, line[1:])
		}
		if len(sample) >= maxSampleLines {
			break
		}
	}
	return strings.Join(sample, "\n")
}

func writeToolResultExtraVisible(b *Block) bool {
	if b.ToolName != tools.NameWrite && b.ToolName != tools.NameDelete {
		return false
	}
	if b.toolResultIsError() || b.toolResultIsCancelled() || !b.ResultDone || strings.TrimSpace(b.ResultContent) == "" {
		return false
	}
	s := strings.TrimSpace(b.ResultContent)
	if strings.Contains(s, "LSP:") || strings.Contains(s, "LSP errors detected") || strings.Contains(s, "<diagnostics") {
		return true
	}
	if lspDiagLineRe.MatchString(s) {
		return true
	}
	return strings.Contains(s, "\n")
}

func renderLSPDiagnosticsLines(content, indent string, width int) []string {
	var out []string
	for line := range strings.SplitSeq(strings.TrimRight(content, "\n"), "\n") {
		var st lipgloss.Style
		if m := lspSeverityRe.FindStringSubmatch(line); len(m) == 2 {
			switch m[1] {
			case "E":
				st = LSPErrorStyle
			case "W":
				st = LSPWarnStyle
			case "I":
				st = LSPInfoStyle
			default:
				st = LSPHintStyle
			}
		} else if lspDiagnosticsOmittedLineRe.MatchString(line) {
			st = DimStyle
		} else if strings.Contains(line, "LSP:") || strings.Contains(line, "LSP errors detected") || strings.Contains(line, "<diagnostics") || lspDiagLineRe.MatchString(strings.TrimSpace(line)) {
			st = LSPErrorStyle
		} else {
			st = ToolResultExpandedStyle
		}
		displayLine := strings.TrimSuffix(line, "\r")
		displayLine = expandTabsForDisplay(displayLine, preformattedTabWidth)
		for _, w := range wrapText(displayLine, width) {
			out = append(out, st.Render(indent+w))
		}
	}
	return out
}

type readDisplayLine struct {
	IsCode  bool
	LineNo  string
	Content string
}
