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

	forceExpanded := b.compactToolResultForceExpanded(contentWidth)
	expanded := b.ToolCallDetailExpanded || forceExpanded
	canExpand := b.searchResultCanExpand()
	prefix := b.renderToolPrefixForExpanded(spinnerFrame, expanded)
	// A force-expanded card is stuck open (the toggle refuses to collapse it),
	// so it must not claim a marker the toggle cannot honour.
	if b.ResultDone && canExpand && !forceExpanded {
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

	if kind := toolOutcomeKindOf(b); kind == toolOutcomeError {
		appendToolOutcomeBody(&result, kind, toolDisplayResultContent(b), contentWidth, expanded)
		result = appendToolElapsedToHeader(result, b, cardWidth)
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}
	if b.toolResultIsCancelled() {
		appendToolOutcomeBody(&result, toolOutcomeCancelled, toolDisplayResultContent(b), contentWidth, expanded)
		result = appendToolElapsedToHeader(result, b, cardWidth)
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}

	if !showInline && !expanded && strings.TrimSpace(summary) != "" {
		// Short result without a count summary: show the raw lines directly.
		for line := range strings.SplitSeq(strings.TrimRight(sanitizeToolDisplayText(summary), "\n"), "\n") {
			for _, wrapped := range wrapText(line, contentWidth) {
				result = append(result, DimStyle.Render("    "+wrapped))
			}
		}
		result = appendToolElapsedToHeader(result, b, cardWidth)
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

	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

func (b *Block) searchResultSummaryLine() (string, bool) {
	if b == nil {
		return "", false
	}
	content := b.stripResultNotes(b.ResultContent)
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	switch tools.NormalizeName(b.ToolName) {
	case tools.NameGrep:
		meta := parseGrepResultMeta(content)
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
		meta := parseGlobResultMeta(content)
		if meta.Files == 0 {
			if strings.HasPrefix(strings.TrimSpace(content), "No files matched") {
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
	if b == nil {
		return false
	}
	content := b.stripResultNotes(b.ResultContent)
	if strings.TrimSpace(content) == "" {
		return false
	}
	switch tools.NormalizeName(b.ToolName) {
	case tools.NameGrep:
		meta := parseGrepResultMeta(content)
		if meta.NoMatches {
			return meta.Notes > 0 || meta.Fallback || meta.Truncated || meta.Skipped > 0
		}
		return meta.Matches > 0 || meta.Notes > 0 || meta.Fallback || meta.Truncated || meta.Skipped > 0
	case tools.NameGlob:
		meta := parseGlobResultMeta(content)
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
			parts = append(parts, fmt.Sprintf("%d %s", files, pluralizeToolCount("file", files)))
		}
	}
	if meta.Added > 0 || meta.Removed > 0 {
		switch {
		case meta.Added > 0 && meta.Removed > 0:
			parts = append(parts, fmt.Sprintf("+%d -%d lines", meta.Added, meta.Removed))
		case meta.Added > 0:
			parts = append(parts, fmt.Sprintf("+%d %s", meta.Added, pluralizeToolCount("line", meta.Added)))
		default:
			parts = append(parts, fmt.Sprintf("-%d %s", meta.Removed, pluralizeToolCount("line", meta.Removed)))
		}
	}
	if !b.toolResultIsError() && !b.toolResultIsCancelled() && toolResultContainsLSPDiagnostics(b.ResultContent) {
		if n := countLSPDiagnosticLines(b.ResultContent); n > 0 {
			parts = append(parts, fmt.Sprintf("%d diagnostics", n))
		} else {
			parts = append(parts, "diagnostics")
		}
	}
	// Error results return no summary: the collapsed card renders the ↳ Error
	// block instead, and the header must not repeat "· error".
	return strings.Join(parts, " · ")
}
