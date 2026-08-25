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

	prefix := b.renderToolPrefixForExpanded(spinnerFrame, b.ToolCallDetailExpanded)
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	keys, vals := parseToolArgs(b.Content)
	mainPart, grayPart := b.formatToolHeaderPartsWithParsed(keys, vals)
	if strings.TrimSpace(b.ResultContent) == "" {
		paramSummary := extractToolParamsWithParsed(keys, vals, cardWidth-16)
		headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, paramSummary, cardWidth-4)
	} else {
		headerLine = appendToolHeaderSummary(headerLine, mainPart, grayPart, "", cardWidth-4)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, b.toolExecutionIsQueued() && b.ToolQueuedByExecutionEvent, b.toolExecutionIsRunning())
	result := []string{headerLine}

	expanded := b.ToolCallDetailExpanded || b.compactToolResultForceExpanded(contentWidth)

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

	if summary, showInline := b.searchResultSummaryLine(); summary != "" && !b.toolExecutionIsQueued() {
		if !showInline && !expanded {
			// summary is the raw result rendered verbatim (dimmed body style);
			// in expanded mode the full result body below renders it, so skip
			// the duplicate summary here.
			lines := strings.Split(strings.TrimRight(sanitizeToolDisplayText(summary), "\n"), "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) == "" {
					continue
				}
				for _, wrapped := range wrapText(line, contentWidth) {
					result = append(result, DimStyle.Render("    "+wrapped))
				}
			}
			result = appendToolElapsedFooter(result, b)
			return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
		}
		if showInline || !expanded {
			suffix := ""
			if !expanded && strings.TrimSpace(b.ResultContent) != "" {
				if b.searchResultCanExpand() {
					suffix = " · [space] expand"
				}
			}
			for _, wrapped := range wrapText(summary+suffix, contentWidth) {
				result = append(result, ToolResultStyle.Render("  ↳ "+wrapped))
			}
		}
	}

	if expanded && strings.TrimSpace(b.ResultContent) != "" {
		lines := strings.Split(strings.TrimRight(sanitizeToolDisplayText(toolDisplayResultContent(b)), "\n"), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			for _, wrapped := range wrapText(line, contentWidth) {
				result = append(result, DimStyle.Render("    "+wrapped))
			}
		}
		result = append(result, renderToolCollapseHint(toolHintIndent))
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
			return strings.TrimSpace(b.ResultContent), false
		}
		if meta.Matches <= 1 && !meta.Fallback && !meta.Truncated && meta.Skipped == 0 {
			return strings.TrimSpace(b.ResultContent), false
		}
		parts := make([]string, 0, 4)
		if meta.Matches > 0 {
			label := "matches"
			if meta.Matches == 1 {
				label = "match"
			}
			if meta.Truncated {
				label = "matches shown"
			}
			parts = append(parts, fmt.Sprintf("%d %s", meta.Matches, label))
		} else if meta.EmptyResult || meta.Matches == 0 {
			return strings.TrimSpace(b.ResultContent), false
		}
		if meta.Files > 0 {
			filesLabel := "files"
			if meta.Files == 1 {
				filesLabel = "file"
			}
			parts = append(parts, fmt.Sprintf("%d %s", meta.Files, filesLabel))
		}
		if meta.Skipped > 0 {
			parts = append(parts, fmt.Sprintf("%d paths skipped", meta.Skipped))
		}
		if meta.Fallback {
			parts = append(parts, "literal fallback")
		}
		if meta.Truncated {
			parts = append(parts, "truncated")
		}
		return strings.Join(parts, " · "), true
	case tools.NameGlob:
		meta := parseGlobResultMeta(b.ResultContent)
		if meta.Files == 0 && !meta.Truncated && meta.Artifact == "" {
			return strings.TrimSpace(b.ResultContent), false
		}
		if meta.Files <= 1 && !meta.Truncated && meta.Artifact == "" {
			return strings.TrimSpace(b.ResultContent), false
		}
		parts := make([]string, 0, 3)
		filesLabel := "files"
		if meta.Files == 1 {
			filesLabel = "file"
		}
		parts = append(parts, fmt.Sprintf("%d %s", meta.Files, filesLabel))
		if meta.Truncated {
			parts = append(parts, "truncated")
		}
		if meta.Artifact != "" {
			parts = append(parts, meta.Artifact)
		}
		return strings.Join(parts, " · "), true
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
		return parseGrepResultMeta(b.ResultContent).HasDetails
	case tools.NameGlob:
		return parseGlobResultMeta(b.ResultContent).HasDetails
	default:
		return false
	}
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
	} else if path := strings.TrimSpace(b.diffToolFilePathWithTargets(applyPatchTargets)); path != "" {
		parts = append(parts, b.displayToolPath(path))
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
