package tui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

var (
	// diffHunkHeaderRe parses unified diff hunk header: @@ -oldStart,oldCount +newStart,newCount @@
	diffHunkHeaderRe = regexp.MustCompile(`^@@ -(\d+),(\d+) \+(\d+),(\d+) @@`)

	// diffAddBg / diffDelBg: subtle line backgrounds (opencode-style) so syntax highlighting stays readable.
	diffAddBg = currentTheme.DiffAddLineBg
	diffDelBg = currentTheme.DiffDelLineBg

	mdListLineRe  = regexp.MustCompile(`(?m)^[\t ]*[-*+][\t ]+\S`)
	lspDiagLineRe = regexp.MustCompile(`^\s*\d+:\d+`)

	// lspSeverityRe matches "[E]", "[W]", "[I]", "[H]" prefixes in LSP diagnostic output.
	lspSeverityRe = regexp.MustCompile(`^\s*\[(E|W|I|H)\]`)
)

// maxToolCallCompactResultLines is the default visible height for generic tool output until space expands.
const maxToolCallCompactResultLines = 10

const (
	toolResultIndent = "    "
	toolHintIndent   = "    "
)

var activeToolSpinnerSegments = [...]string{"▖", "▘", "▝", "▗"}

const queuedToolGlyph = "⋯"

func toolUsesCompactDetailToggle(toolName string) bool {
	switch toolName {
	case "Write", "Edit", "Read", "TodoWrite", "Question", "Delegate":
		return false
	}
	return true
}

func toolResultLooksLikeMarkdown(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "#") {
		return true
	}
	if strings.Contains(s, "```") {
		return true
	}
	return mdListLineRe.MatchString(s)
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
	trimmed := strings.TrimSpace(summary)
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
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	if toolResultLooksLikeMarkdown(trimmed) {
		return renderMarkdownContent(trimmed, width)
	}
	return wrapText(trimmed, width)
}

func normalizedCompactToolPreviewText(s string) string {
	trimmed := strings.TrimSpace(s)
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

func compactToolPreviewDuplicatesResult(previewLine string, resultLines []string) bool {
	previewNorm := normalizedCompactToolPreviewText(previewLine)
	if previewNorm == "" || len(resultLines) == 0 {
		return false
	}
	previewValueNorm := ""
	if idx := strings.Index(previewLine, ":"); idx >= 0 {
		previewValueNorm = normalizedCompactToolPreviewText(previewLine[idx+1:])
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
	if toolResultLooksLikeMarkdown(trimmed) {
		return len(renderMarkdownContent(trimmed, width))
	}
	return len(wrapText(trimmed, width))
}

func toolExpandedResultLines(displayResult string, width int, expanded bool, allowMarkdown bool) ([]string, int, bool) {
	trimmed := strings.TrimSpace(displayResult)
	if trimmed == "" {
		return nil, 0, false
	}
	if allowMarkdown && expanded && toolResultLooksLikeMarkdown(trimmed) {
		return renderMarkdownContent(trimmed, width), 0, true
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
		total := len(toolExpandedTextLines(displayResult, width))
		if total > visible {
			hidden = total - visible
		}
	}
	return out, hidden, false
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
		for _, line := range wrapText(detail, width) {
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
	trimmed := strings.TrimSpace(content)
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
		if timedOut := bashTimeoutSummary(b.ResultContent); timedOut != "" {
			return timedOut, true
		}
		if line := bashFirstNonEmptyLine(bashErrorBody(b.ResultContent)); line != "" {
			return truncateOneLine(line, 120), true
		}
		if line := bashFirstNonEmptyLine(b.ResultContent); line != "" {
			return truncateOneLine(line, 120), true
		}
		if exit := bashExitCodeFromError(b.ResultContent); exit != "" {
			return exit, true
		}
		return "failed", true
	}
	_, stdout := bashSplitResultStreams(b)
	if line := bashFirstNonEmptyLine(stdout); line != "" {
		return truncateOneLine(line, 120), false
	}
	if line := bashFirstNonEmptyLine(strings.TrimSpace(b.ResultContent)); line != "" {
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
	idx := strings.Index(trimmed, marker)
	if idx < 0 {
		return trimmed
	}
	body := strings.TrimSpace(trimmed[idx+len(marker):])
	return body
}

func bashTimeoutSummary(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "timed out after") {
		prefix := strings.TrimPrefix(trimmed, "command ")
		if i := strings.Index(prefix, " after output:"); i >= 0 {
			return prefix[:i]
		}
		if i := strings.Index(prefix, "\n"); i >= 0 {
			return prefix[:i]
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
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
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
	if b.toolResultIsCancelled() {
		return "Cancelled"
	}
	if !b.ResultDone {
		return ""
	}

	trimmed := strings.TrimSpace(b.ResultContent)
	switch b.ToolName {
	case "Bash":
		// Bash expands with explicit exit-code detail, so avoid a redundant summary like "Passed".
		if b.toolResultIsError() {
			return ""
		}
		if trimmed == "" {
			return ""
		}
		return ""
	case "Spawn":
		if b.toolResultIsError() {
			return "Failed"
		}
		return "Started"
	case "SpawnStop":
		if b.toolResultIsError() {
			return "Failed"
		}
		return "Stopped"
	case "Delegate":
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
	case "Grep":
		if b.toolResultIsError() {
			return "Search failed"
		}
		if trimmed == "No matches found." {
			return ""
		}
		count := 0
		for _, line := range strings.Split(strings.TrimRight(trimmed, "\n"), "\n") {
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
	case "Glob":
		if b.toolResultIsError() {
			return "Search failed"
		}
		if trimmed == "No files matched the pattern." {
			return ""
		}
		count := 0
		for _, line := range strings.Split(strings.TrimRight(trimmed, "\n"), "\n") {
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
	case "Cancel":
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
	case "Notify":
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
		return ""
	}
}

func renderQueuedToolHeaderBadge(line string, width int) string {
	const badgeText = "Queued"
	trimmed := strings.TrimRight(line, " ")
	badge := DimStyle.Render(badgeText)
	badgeWidth := runewidth.StringWidth(badgeText)
	lineWidth := ansi.StringWidth(trimmed)
	if lineWidth == 0 {
		return badge
	}
	if width <= 0 {
		width = lineWidth + 1 + badgeWidth
	}
	availableGap := width - lineWidth - badgeWidth
	if availableGap >= 2 {
		return trimmed + strings.Repeat(" ", availableGap) + badge
	}
	return trimmed + " " + badge
}

func renderToolExpandHint(indent string, hidden int) string {
	if hidden <= 0 {
		return ""
	}
	return DimStyle.Render(fmt.Sprintf("%s── %d more lines · [space] expand ──", indent, hidden))
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
	highlighted := h.highlightSnippet(strings.Join(lines, "\n"), bgTerm)
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

func isReadLineNumberPrefix(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parseReadDisplayLines(result string) ([]readDisplayLine, string) {
	if result == "" {
		return nil, ""
	}
	rawLines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	rows := make([]readDisplayLine, 0, len(rawLines))
	codeLines := make([]string, 0, len(rawLines))

	for _, line := range rawLines {
		line = strings.TrimSuffix(line, "\r")
		if before, after, ok := strings.Cut(line, "\t"); ok && isReadLineNumberPrefix(before) {
			content := after
			rows = append(rows, readDisplayLine{IsCode: true, LineNo: strings.TrimSpace(before), Content: content})
			codeLines = append(codeLines, content)
			continue
		}
		rows = append(rows, readDisplayLine{Content: line})
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

func writeEditToolResultExtraVisible(b *Block) bool {
	if b.ToolName != "Write" && b.ToolName != "Edit" && b.ToolName != "Delete" {
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
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
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
		} else if strings.Contains(line, "LSP:") || strings.Contains(line, "LSP errors detected") || strings.Contains(line, "<diagnostics") || lspDiagLineRe.MatchString(strings.TrimSpace(line)) {
			st = LSPErrorStyle
		} else {
			st = ToolResultExpandedStyle
		}
		trimmed := strings.TrimSpace(line)
		for _, w := range wrapText(trimmed, width) {
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
