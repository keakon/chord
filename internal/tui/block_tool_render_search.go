package tui

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

func (b *Block) renderSearchResultToolCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	contentWidth := metrics.contentWidth

	expanded := b.ToolCallDetailExpanded || b.compactToolResultForceExpanded(contentWidth)
	canExpand := b.searchResultCanExpand()
	prefix := b.renderToolPrefixForExpanded(spinnerFrame, expanded)
	if b.ResultDone && canExpand {
		prefix = renderToolDisclosurePrefix(prefix, expanded)
	}
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	keys, vals := parseToolArgs(b.Content)
	mainPart, grayPart := b.formatToolHeaderPartsWithParsed(keys, vals)
	// The key count summary (matches/files) is merged into the
	// header so a collapsed search card is a single line like Read. Short
	// results without a summary keep their inline body below.
	summary, showInline := "", false
	if !b.toolResultIsError() && !b.toolResultIsCancelled() && !b.toolExecutionIsQueued() {
		summary, showInline = b.searchResultSummaryLine()
	}
	headerSummary := ""
	if showInline {
		headerSummary = summary
	}
	if strings.TrimSpace(b.ResultContent) == "" {
		paramSummary := extractToolParamsWithParsed(keys, vals, cardWidth-16)
		headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary, cardWidth-4)
	} else {
		headerLine = appendSearchHeaderSummary(headerLine, mainPart, grayPart, headerSummary, cardWidth-4)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
		result = append(result, ErrorStyle.Render("  ↳ Error:"))
		for _, line := range wrapText(sanitizeToolDisplayText(toolDisplayResultContent(b)), contentWidth) {
			result = append(result, ErrorStyle.Render("    "+line))
		}
		result = appendToolElapsedFooter(result, b)
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}
	if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
		result = append(result, DimStyle.Render("  ↳ Cancelled"))
		if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
			for _, line := range wrapText(sanitizeToolDisplayText(detail), contentWidth) {
				result = append(result, DimStyle.Render("    "+line))
			}
		}
		result = appendToolElapsedFooter(result, b)
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	if !showInline && !expanded && strings.TrimSpace(summary) != "" {
		// Short result without a count summary: show the raw lines directly.
		for line := range strings.SplitSeq(strings.TrimRight(sanitizeToolDisplayText(summary), "\n"), "\n") {
			for _, wrapped := range wrapText(line, contentWidth) {
				result = append(result, DimStyle.Render("    "+wrapped))
			}
		}
		result = appendToolElapsedFooter(result, b)
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	if expanded && strings.TrimSpace(b.ResultContent) != "" {
		for line := range strings.SplitSeq(strings.TrimRight(sanitizeToolDisplayText(toolDisplayResultContent(b)), "\n"), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			for _, wrapped := range wrapText(line, contentWidth) {
				result = append(result, DimStyle.Render("    "+wrapped))
			}
		}
	}

	result = appendToolElapsedFooter(result, b)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func (b *Block) searchResultSummaryLine() (string, bool) {
	if b == nil || strings.TrimSpace(b.ResultContent) == "" {
		return "", false
	}
	switch tools.NormalizeName(b.ToolName) {
	case tools.NameGrep:
		meta := parseGrepResultMeta(b.ResultContent)
		if meta.NoMatches {
			return "No matches", true
		}
		if meta.Matches == 0 {
			return "", false
		}
		parts := []string{fmt.Sprintf("%d %s", meta.Matches, pluralizeToolCount("match", meta.Matches))}
		if meta.Files > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", meta.Files, pluralizeToolCount("file", meta.Files)))
		}
		return strings.Join(parts, " · "), true
	case tools.NameGlob:
		meta := parseGlobResultMeta(b.ResultContent)
		if meta.Files == 0 {
			if strings.HasPrefix(strings.TrimSpace(b.ResultContent), "No files matched") {
				return "No files", true
			}
			return "", false
		}
		return fmt.Sprintf("%d %s", meta.Files, pluralizeToolCount("file", meta.Files)), true
	default:
		return "", false
	}
}

func (b *Block) searchResultCanExpand() bool {
	if b == nil || strings.TrimSpace(b.ResultContent) == "" {
		return false
	}
	switch tools.NormalizeName(b.ToolName) {
	case tools.NameGrep:
		meta := parseGrepResultMeta(b.ResultContent)
		if meta.NoMatches {
			return meta.Notes > 0 || meta.Fallback || meta.Truncated || meta.Skipped > 0
		}
		return meta.Matches > 0 || meta.Notes > 0 || meta.Fallback || meta.Truncated || meta.Skipped > 0
	case tools.NameGlob:
		meta := parseGlobResultMeta(b.ResultContent)
		return meta.Files > 0 || meta.Truncated || meta.Artifact != ""
	default:
		return false
	}
}

func pluralizeToolCount(noun string, count int) string {
	if count == 1 {
		return noun
	}
	if noun == "match" {
		return "matches"
	}
	return noun + "s"
}

func (b *Block) fileDiffSummaryLine(applyPatchTargets []tools.ApplyPatchDisplayTarget, displayDiff string) string {
	if b == nil {
		return ""
	}
	meta := parseDiffResultMeta(displayDiff)
	parts := make([]string, 0, 3)
	if tools.NormalizeName(b.ToolName) == tools.NameApplyPatch {
		files := meta.Files
		if files == 0 {
			files = len(applyPatchTargets)
		}
		if files > 0 {
			parts = append(parts, fmt.Sprintf("%d files", files))
		}
	}
	if meta.Added > 0 || meta.Removed > 0 {
		parts = append(parts, fmt.Sprintf("+%d -%d lines", meta.Added, meta.Removed))
	}
	if !b.toolResultIsError() && !b.toolResultIsCancelled() && toolResultContainsLSPDiagnostics(b.ResultContent) {
		if n := countLSPDiagnosticLines(b.ResultContent); n > 0 {
			parts = append(parts, fmt.Sprintf("%d diagnostics", n))
		} else {
			parts = append(parts, "diagnostics")
		}
	}
	if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
		parts = append(parts, "error")
	}
	return strings.Join(parts, " · ")
}
