package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/tools"
)

const (
	diffSnippetMergeGapCols       = 6
	diffSnippetContextCols        = 12
	diffSnippetMinContextCols     = 3
	maxInlineSnippetClusters      = 2
	maxTwoLineSnippetClusters     = 3
	defaultSingleLineDiffColumns  = 200
	defaultSnippetSummaryMinWidth = 12
)

var singleLineDiffColumnsLimit = defaultSingleLineDiffColumns

func SetSingleLineDiffColumnsLimit(limit int) {
	if limit <= 0 {
		singleLineDiffColumnsLimit = defaultSingleLineDiffColumns
		return
	}
	singleLineDiffColumnsLimit = limit
}

type diffSegmentSpan struct {
	Text               string
	Kind               string
	StartCol, EndCol   int
	StartByte, EndByte int
}

type diffSnippetWindow struct {
	StartCol int
	EndCol   int
}

type diffByteRange struct {
	Start int
	End   int
}

type diffOneSidedSpan struct {
	Prefix    string
	Change    string
	Suffix    string
	StartCol  int
	EndCol    int
	LineWidth int
}

func appendApplyPatchToolUnifiedDiffPair(result *[]string, oldLine, newLine string, oldLineNum, newLineNum, diffWidth int, hl *codeHighlighter) {
	// Diff bodies come from file contents, which can legitimately carry
	// orphaned variation selectors (left by editors or earlier tool runs).
	// Strip them before inline-diff spans and width math so the spans stay
	// aligned with what the terminal paints.
	oldLine = tools.StripOrphanVariationSelectors(oldLine)
	newLine = tools.StripOrphanVariationSelectors(newLine)
	formatLineNum := func(n int) string { return fmt.Sprintf("%4d ", n) }
	if lines := renderInlineDiffLine(oldLine, newLine, diffWidth, hl); lines != nil {
		if strings.HasPrefix(lines[0], "+") {
			*result = append(*result, "  "+DimStyle.Render(formatLineNum(newLineNum))+lines[0])
		} else {
			*result = append(*result, "  "+DimStyle.Render(formatLineNum(oldLineNum))+lines[0])
		}
		return
	}
	oldSegs, newSegs := tools.InlineDiff(oldLine, newLine)
	oldCode := renderHighlightedSnippetLine(oldLine, filterDiffSpansByKind(buildDiffSegmentSpans(oldSegs), "delete"), diffWidth-1, hl, diffDelBg)
	newCode := renderHighlightedSnippetLine(newLine, filterDiffSpansByKind(buildDiffSegmentSpans(newSegs), "insert"), diffWidth-1, hl, diffAddBg)
	*result = append(*result,
		"  "+DimStyle.Render(formatLineNum(oldLineNum))+DiffDelStyle.Render("-")+oldCode,
		"  "+DimStyle.Render(formatLineNum(newLineNum))+DiffAddStyle.Render("+")+newCode,
	)
}

func appendApplyPatchToolUnifiedDiffLine(result *[]string, body string, lineNum, diffWidth int, hl *codeHighlighter, added bool) {
	body = tools.StripOrphanVariationSelectors(body)
	bg := diffDelBg
	marker := DiffDelStyle.Render("-")
	if added {
		bg = diffAddBg
		marker = DiffAddStyle.Render("+")
	}
	code := renderHighlightedSnippetLine(body, []diffSegmentSpan{{StartCol: 0, EndCol: diffTextWidth(body)}}, diffWidth-1, hl, bg)
	*result = append(*result, "  "+DimStyle.Render(fmt.Sprintf("%4d ", lineNum))+marker+code)
}

func nextNonEmptyUnifiedDiffLine(lines []string, index int) int {
	for index < len(lines) && lines[index] == "" {
		index++
	}
	return index
}

// renderFileDiffCall renders content-changing tool calls with a diff-style view.
func (b *Block) renderFileDiffCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	// Parse the apply_patch targets once per render; both the header path and
	// the body below need them, and parsing re-reads the patch args JSON.
	applyPatchTargets := b.applyPatchTargets()
	successfulApplyPatch := b.ToolName == tools.NameApplyPatch && b.ResultDone && !b.toolResultIsError() && !b.toolResultIsCancelled()
	applyPatchNoChanges := successfulApplyPatch && strings.Contains(b.ResultContent, "No net file changes")
	displayDiff := b.Diff
	if successfulApplyPatch {
		displayDiff = b.applyPatchDisplayDiff(applyPatchTargets)
	}
	hasOperationSummaries := successfulApplyPatch && applyPatchHasSummaryOnlyTargets(applyPatchTargets)
	filePath := b.diffToolFilePathWithTargets(applyPatchTargets)
	if filePath != "" {
		filePath = b.displayToolPath(filePath)
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	hasDisclosure := !b.toolResultIsCancelled() && (strings.TrimSpace(displayDiff) != "" || hasOperationSummaries || len(applyPatchTargets) > 0)
	if b.ResultDone && hasDisclosure {
		prefix = renderToolDisclosurePrefix(prefix, !b.Collapsed)
	}
	var result []string
	replaceArgs, hasReplaceArgs := replaceEditArgs{}, false
	var headerOpts []string
	if b.ToolName == tools.NameEdit {
		replaceArgs, hasReplaceArgs = parseReplaceEditArgs(b.editPatchArgsJSON())
		if hasReplaceArgs && replaceArgs.ReplaceAll != nil && *replaceArgs.ReplaceAll {
			headerOpts = append(headerOpts, "replace_all=true")
		}
	}
	headerOpts = append(headerOpts, b.diagnosticHeaderOptions()...)
	// Success renders as a single header line like read/grep/glob/write in
	// both fold states: path, parameters and the +/- line count summary merge
	// into the header (parameters drop first when narrow); error and cancelled
	// details stay in the body below.
	headerSummary := ""
	if !b.toolResultIsError() && !b.toolResultIsCancelled() {
		headerSummary = b.fileDiffSummaryLine(applyPatchTargets, displayDiff)
		if applyPatchNoChanges {
			if headerSummary != "" {
				headerSummary += " · "
			}
			headerSummary += "No changes"
		}
		if headerSummary == "" && strings.TrimSpace(b.ResultContent) != "" {
			displayResult := sanitizeToolDisplayText(toolCollapsedResultContent(b.ToolName, toolDisplayResultContent(b)))
			headerSummary = truncateOneLine(displayResult, cardWidth-26)
		}
	}
	headerLine := appendSearchHeaderSummary(renderToolHeaderLine(prefix, b.ToolName), filePath, mergeHeaderOptions("", headerOpts), headerSummary, cardWidth-4)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)
	if b.Collapsed {
		if b.toolResultIsError() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			for _, line := range wrapText(sanitizeToolDisplayText(toolDisplayResultContent(b)), cardWidth-8) {
				result = append(result, ErrorStyle.Render("    "+line))
			}
		} else if b.toolResultIsCancelled() && strings.TrimSpace(b.ResultContent) != "" {
			result = append(result, DimStyle.Render("  ↳ Cancelled"))
			if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
				for _, line := range wrapText(sanitizeToolDisplayText(detail), cardWidth-8) {
					result = append(result, DimStyle.Render("    "+line))
				}
			}
		}
		return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}
	diffLines := strings.Split(displayDiff, "\n")
	diffFileCount := unifiedDiffFileCount(diffLines)
	groupedApplyPatchDiff := b.ToolName == tools.NameApplyPatch && (diffFileCount > 1 || hasOperationSummaries && diffFileCount > 0)
	if b.ToolName == tools.NameApplyPatch {
		if hasOperationSummaries {
			result = appendApplyPatchOperationSummaries(result, applyPatchTargets, cardWidth-4)
		} else if !groupedApplyPatchDiff {
			result = appendApplyPatchTargetLines(result, applyPatchTargets, cardWidth-4)
		}
		if strings.TrimSpace(displayDiff) == "" && !applyPatchNoChanges && !b.toolResultIsError() && !b.toolResultIsCancelled() &&
			!applyPatchOnlyMoveOrDeleteTargets(applyPatchTargets) {
			result = appendApplyPatchPreview(result, b, filePath, cardWidth-4)
		}
		if applyPatchNoChanges {
			result = append(result, DimStyle.Render("  ↳ No changes"))
		}
		if strings.TrimSpace(displayDiff) != "" && b.toolResultIsError() {
			result = append(result, ToolResultExpandedStyle.Render("  ↳ Applied changes:"))
		}
	}
	const diffLineNumWidth = 5
	diffWidth := max(cardWidth-4-diffLineNumWidth, 10)
	// Sample the diff content once; the initial highlighter and every per-file
	// section highlighter of a multi-file patch share the same sample.
	diffSample := diffContentSample(displayDiff)
	hl := ensureCodeHighlighter(&b.codeHL, filePath, diffSample)
	seenHunk := false
	renderedDiffFileCount := 0
	var oldLineNum, newLineNum int
	if !b.toolResultIsCancelled() {
		for i := 0; i < len(diffLines); i++ {
			line := diffLines[i]
			if line == "" {
				continue
			}
			var rendered string
			switch {
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				next := nextNonEmptyUnifiedDiffLine(diffLines, i+1)
				nextIsDeletion := next < len(diffLines) && strings.HasPrefix(diffLines[next], "-") && !strings.HasPrefix(diffLines[next], "---")
				nextIsAddition := next < len(diffLines) && strings.HasPrefix(diffLines[next], "+") && !strings.HasPrefix(diffLines[next], "+++")
				if nextIsAddition {
					afterAdd := nextNonEmptyUnifiedDiffLine(diffLines, next+1)
					if afterAdd >= len(diffLines) || !strings.HasPrefix(diffLines[afterAdd], "+") || strings.HasPrefix(diffLines[afterAdd], "+++") {
						appendApplyPatchToolUnifiedDiffPair(&result, line[1:], diffLines[next][1:], oldLineNum, newLineNum, diffWidth, hl)
						oldLineNum++
						newLineNum++
						i = next
						continue
					}
				}
				if !nextIsDeletion && !nextIsAddition {
					appendApplyPatchToolUnifiedDiffLine(&result, line[1:], oldLineNum, diffWidth, hl, false)
					oldLineNum++
					i = next - 1
					continue
				}
				j := i
				var delBodies, addBodies []string
				for j < len(diffLines) {
					l := diffLines[j]
					if l == "" {
						j++
						continue
					}
					if strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "---") {
						delBodies = append(delBodies, l[1:])
						j++
						continue
					}
					break
				}
				addJ := j
				for addJ < len(diffLines) {
					l := diffLines[addJ]
					if l == "" {
						addJ++
						continue
					}
					if strings.HasPrefix(l, "+") && !strings.HasPrefix(l, "+++") {
						addBodies = append(addBodies, l[1:])
						addJ++
						continue
					}
					break
				}
				if len(addBodies) > 0 && len(delBodies) == len(addBodies) {
					for k := range delBodies {
						appendApplyPatchToolUnifiedDiffPair(&result, delBodies[k], addBodies[k], oldLineNum, newLineNum, diffWidth, hl)
						oldLineNum++
						newLineNum++
					}
					i = addJ - 1
					continue
				}
				if len(addBodies) > 0 {
					for _, body := range delBodies {
						appendApplyPatchToolUnifiedDiffLine(&result, body, oldLineNum, diffWidth, hl, false)
						oldLineNum++
					}
					for _, body := range addBodies {
						appendApplyPatchToolUnifiedDiffLine(&result, body, newLineNum, diffWidth, hl, true)
						newLineNum++
					}
					i = addJ - 1
					continue
				}
				for _, body := range delBodies {
					appendApplyPatchToolUnifiedDiffLine(&result, body, oldLineNum, diffWidth, hl, false)
					oldLineNum++
				}
				i = j - 1
				continue
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				appendApplyPatchToolUnifiedDiffLine(&result, line[1:], newLineNum, diffWidth, hl, true)
				newLineNum++
				continue
			case strings.HasPrefix(line, "@@"):
				if seenHunk {
					sep := DimStyle.Render("  ─────────────")
					result = append(result, "  "+sep)
				}
				seenHunk = true
				hunkLine, _, _ := strings.Cut(line, "\n")
				if m := diffHunkHeaderRe.FindStringSubmatch(hunkLine); len(m) == 3 {
					oldStart, _ := strconv.Atoi(m[1])
					newStart, _ := strconv.Atoi(m[2])
					oldLineNum, newLineNum = oldStart, newStart
				} else {
					// An unparsable header must not leave the previous hunk's
					// counters in place: every following line would then get a
					// confidently wrong gutter number. Drop to 0 so the gutter
					// reads as unknown instead of misleading.
					oldLineNum, newLineNum = 0, 0
				}
				continue
			case strings.HasPrefix(line, "--- "):
				if groupedApplyPatchDiff && i+1 < len(diffLines) && strings.HasPrefix(diffLines[i+1], "+++ ") {
					marker, path, syntaxPath := b.applyPatchDiffSectionDisplay(applyPatchTargets, line, diffLines[i+1])
					if renderedDiffFileCount > 0 {
						result = append(result, "  "+DimStyle.Render("─────────────"))
					}
					filePrefix := "  ↳ " + marker + " "
					fileLine := truncateApplyPatchDisplayLine(filePrefix+path, cardWidth)
					result = append(result, ToolResultExpandedStyle.Render(filePrefix)+DimStyle.Render(strings.TrimPrefix(fileLine, filePrefix)))
					seenHunk = false
					oldLineNum, newLineNum = 0, 0
					hl = newCodeHighlighterWithLanguage(syntaxPath, diffSample, "")
					renderedDiffFileCount++
					i++
				}
				continue
			case strings.HasPrefix(line, "+++ "):
				continue
			default:
				content := line
				if len(content) > 0 && content[0] == ' ' {
					content = content[1:]
				}
				content = tools.StripOrphanVariationSelectors(content)
				code := renderHighlightedSnippetLine(content, nil, diffWidth-1, hl, "")
				displayLineNum := max(newLineNum, oldLineNum)
				rendered = DimStyle.Render(fmt.Sprintf("%4d ", displayLineNum)) + " " + code
				oldLineNum++
				newLineNum++
			}
			result = append(result, "  "+rendered)
		}
	}
	if (b.ToolName == tools.NameEdit || b.ToolName == tools.NameApplyPatch) && strings.TrimSpace(b.ResultContent) != "" && !b.toolResultIsError() && !b.toolResultIsCancelled() && !toolShouldHideSuccessfulFileOpResult(b) {
		result = append(result, ToolResultExpandedStyle.Render("  ↳ Diagnostics:"))
		result = append(result, renderLSPDiagnosticsLines(editSuccessDiagnosticsContent(b.ResultContent), "    ", cardWidth-4)...)
	}
	if b.toolResultIsError() && b.ResultContent != "" {
		switch b.ToolName {
		case tools.NameApplyPatch:
			sections := splitApplyPatchErrorSections(b.ResultContent)
			if strings.TrimSpace(displayDiff) == "" {
				result = appendApplyPatchPreview(result, b, filePath, cardWidth-4)
				if sections.applied != "" && !hasOperationSummaries {
					result = append(result, ToolResultExpandedStyle.Render("  ↳ Applied changes:"))
					result = appendApplyPatchErrorTextLines(result, sections.applied, cardWidth-4)
				}
			}
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			result = appendApplyPatchErrorTextLines(result, sections.failure, cardWidth-4)
			if sections.diagnostics != "" {
				result = append(result, ToolResultExpandedStyle.Render("  ↳ Diagnostics:"))
				result = append(result, renderLSPDiagnosticsLines(sections.diagnostics, "    ", cardWidth-4)...)
			}
		case tools.NameEdit:
			if strings.TrimSpace(displayDiff) == "" {
				if hasReplaceArgs {
					result = appendReplaceEditPreview(result, replaceArgs, filePath, cardWidth-4)
				} else {
					result = appendEditPatchPreview(result, b.editPatchArgsJSON(), cardWidth-4)
				}
			}
			result = append(result, ErrorStyle.Render("  ↳ Error:"))
			result = append(result, renderLSPDiagnosticsLines(toolErrorDisplayContent(b.ResultContent), "    ", cardWidth-4)...)
		}
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = append(result, DimStyle.Render("  ↳ Cancelled"))
		if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
			result = append(result, renderLSPDiagnosticsLines(detail, "    ", cardWidth-4)...)
		}
	}
	result = appendToolElapsedToHeader(result, b, cardWidth)
	return b.renderToolCardWithIgnoredArgs(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
}

type applyPatchErrorSections struct {
	applied     string
	failure     string
	diagnostics string
}

func splitApplyPatchErrorSections(content string) applyPatchErrorSections {
	content = strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n"))
	if !strings.HasPrefix(content, "apply_patch partially applied:") {
		return applyPatchErrorSections{failure: toolErrorDisplayContent(content)}
	}

	var sections applyPatchErrorSections
	var applied, failure, diagnostics []string
	part := "failure"
	for line := range strings.SplitSeq(content, "\n") {
		switch strings.TrimSpace(line) {
		case "Applied patch:":
			part = "applied"
			continue
		case "Diagnostics:", "Diagnostics summary:":
			part = "diagnostics"
			continue
		case "Not applied:":
			part = "failure"
			failure = append(failure, line)
			continue
		}
		switch part {
		case "applied":
			applied = append(applied, line)
		case "diagnostics":
			diagnostics = append(diagnostics, line)
		default:
			failure = append(failure, line)
		}
	}
	sections.applied = strings.TrimSpace(strings.Join(applied, "\n"))
	sections.failure = strings.TrimSpace(strings.Join(failure, "\n"))
	sections.diagnostics = strings.TrimSpace(strings.Join(diagnostics, "\n"))
	return sections
}

// appendApplyPatchErrorTextLines renders plain-text sections of an apply_patch
// error result (the "Error:" summary, the "Not applied:" details, the trailing
// guidance line) under the "↳ Error:" header. Plain text is not aligned like a
// diff body, so each line wraps at the available width instead of truncating
// with "…": the tail of a long diagnostic (e.g. "hunk not found (1/1); first
// expected complete line: `\t\tif …`") is exactly what the user needs to fix
// the patch, and clipping it hides the actionable information. Matches the
// wrap behavior of renderLSPDiagnosticsLines and the collapsed error path.
func appendApplyPatchErrorTextLines(result []string, content string, width int) []string {
	// displayIndent is prepended BEFORE wrapping so wrapText treats it as the
	// paragraph indent and re-applies it to every continuation line; otherwise
	// wrapped lines would lose alignment with the first line.
	const displayIndent = "    "
	for line := range strings.SplitSeq(strings.TrimRight(content, "\n"), "\n") {
		displayLine := sanitizeToolDisplayText(strings.TrimSuffix(line, "\r"))
		// Expand tabs so an indented code snippet inside the diagnostic (e.g.
		// `\t\tif isFoo(err) {`) aligns to the tab stop and wraps cleanly.
		displayLine = expandTabsForDisplay(displayLine, preformattedTabWidth)
		for _, wl := range wrapText(displayIndent+displayLine, width) {
			result = append(result, ToolResultExpandedStyle.Render(wl))
		}
	}
	return result
}

func unifiedDiffFileCount(lines []string) int {
	count := 0
	for i := 0; i+1 < len(lines); i++ {
		if strings.HasPrefix(lines[i], "--- ") && strings.HasPrefix(lines[i+1], "+++ ") {
			count++
			i++
		}
	}
	return count
}

func (b *Block) applyPatchDisplayDiff(targets []tools.ApplyPatchDisplayTarget) string {
	lines := strings.Split(b.Diff, "\n")
	filtered := make([]string, 0, len(lines))
	foundSection := false
	for i := 0; i < len(lines); {
		if i+1 >= len(lines) || !strings.HasPrefix(lines[i], "--- ") || !strings.HasPrefix(lines[i+1], "+++ ") {
			filtered = append(filtered, lines[i])
			i++
			continue
		}
		foundSection = true
		end := i + 2
		for end < len(lines) {
			if end+1 < len(lines) && strings.HasPrefix(lines[end], "--- ") && strings.HasPrefix(lines[end+1], "+++ ") {
				break
			}
			end++
		}
		if !b.applyPatchDiffSectionIsSummaryOnly(targets, lines[i], lines[i+1]) {
			filtered = append(filtered, lines[i:end]...)
		}
		i = end
	}
	if !foundSection {
		return b.Diff
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func (b *Block) applyPatchDiffSectionIsSummaryOnly(targets []tools.ApplyPatchDisplayTarget, oldHeader, newHeader string) bool {
	oldPath := strings.TrimSpace(strings.TrimPrefix(oldHeader, "--- "))
	newPath := strings.TrimSpace(strings.TrimPrefix(newHeader, "+++ "))
	for _, target := range targets {
		switch {
		case target.Kind == tools.MutationDelete && target.TargetPath == "":
			if oldPath == target.SourcePath && newPath == "/dev/null" {
				return true
			}
		case target.TargetPath != "" && target.Added == 0 && target.Removed == 0:
			if oldPath == target.SourcePath && newPath == target.TargetPath {
				return true
			}
		}
	}
	return newPath == "/dev/null"
}

func (b *Block) applyPatchDiffSectionDisplay(targets []tools.ApplyPatchDisplayTarget, oldHeader, newHeader string) (marker, path, syntaxPath string) {
	oldPath := strings.TrimSpace(strings.TrimPrefix(oldHeader, "--- "))
	newPath := strings.TrimSpace(strings.TrimPrefix(newHeader, "+++ "))
	for _, target := range targets {
		matches := false
		switch {
		case target.TargetPath != "":
			matches = oldPath == target.SourcePath && newPath == target.TargetPath
		case target.Kind == tools.MutationAdd:
			matches = oldPath == "/dev/null" && newPath == target.SourcePath
		case target.Kind == tools.MutationDelete:
			matches = oldPath == target.SourcePath && newPath == "/dev/null"
		default:
			matches = oldPath == target.SourcePath && newPath == target.SourcePath
		}
		if !matches {
			continue
		}
		marker, _ = applyPatchTargetDisplay(target)
		source := b.displayToolPath(target.SourcePath)
		if target.TargetPath != "" {
			targetPath := b.displayToolPath(target.TargetPath)
			return marker, source + " → " + targetPath, targetPath
		}
		return marker, source, source
	}

	oldPath = b.displayToolPath(oldPath)
	newPath = b.displayToolPath(newPath)
	switch {
	case oldPath == "/dev/null":
		return "A", newPath, newPath
	case newPath == "/dev/null":
		return "D", oldPath, oldPath
	case oldPath != newPath:
		return "R", oldPath + " → " + newPath, newPath
	default:
		return "M", oldPath, oldPath
	}
}

func appendEditPatchPreview(result []string, argsJSON string, width int) []string {
	patch := tools.StripOrphanVariationSelectors(editPatchFromArgs(argsJSON))
	if patch == "" {
		return result
	}
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Patch:"))
	// Truncate (not wrap) each patch line, matching the apply_patch preview and
	// the diff body. Diffs/file content are column-aligned; wrapping breaks the
	// +/- gutter alignment and is harder to read than a clipped line.
	for _, line := range editPatchPreviewLines(patch) {
		result = append(result, renderEditPatchPreviewLine(truncateApplyPatchDisplayLine(line, width)))
	}
	return result
}

const patchPreviewCoalesceBytes = 256

// cachedApplyPatchStreamingArgs mirrors streamingToolDisplayArgs for a live
// apply_patch card. Extracting the streaming preview is linear in the
// accumulated args, so re-running it per streamed fragment would be
// quadratic; the preview refreshes at most once per
// patchPreviewCoalesceBytes of growth instead. Complete-JSON and path-only
// displays are stable or cheap and re-evaluate each call.
func (b *Block) cachedApplyPatchStreamingArgs(argsJSON string) string {
	if b.patchPreviewText != "" && len(argsJSON) >= b.patchPreviewLen && len(argsJSON)-b.patchPreviewLen < patchPreviewCoalesceBytes {
		return b.patchPreviewText
	}
	display := applyPatchToolDisplayArgs(argsJSON)
	if display == "" {
		display = applyPatchStreamingPreview(argsJSON)
	}
	b.patchPreviewLen = len(argsJSON)
	b.patchPreviewText = display
	if display != "" {
		return display
	}
	if path := tools.ExtractEditPathFromArgs([]byte(argsJSON)); path != "" {
		return fileToolPathDisplayArgs(path)
	}
	return ""
}

func appendApplyPatchPreview(result []string, b *Block, filePath string, width int) []string {
	argsJSON := b.editPatchArgsJSON()
	// Strip orphaned variation selectors so the highlighted preview lines
	// measure the same width the terminal paints. The streaming fallback is
	// already sanitized; stripping again is idempotent.
	patch := tools.StripOrphanVariationSelectors(editPatchFromArgs(argsJSON))
	if patch == "" {
		// Args may still be streaming: the JSON is not parseable yet, but the
		// patch text itself is a valid live preview (see
		// applyPatchStreamingPreview).
		patch = applyPatchStreamingPreview(argsJSON)
	}
	if patch == "" {
		return result
	}
	hl := ensureCodeHighlighterWithLanguage(&b.previewHL, filePath, applyPatchCodeSample(patch), "")
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Requested patch:"))
	for _, line := range editPatchPreviewLines(patch) {
		result = append(result, renderApplyPatchPreviewLine(line, width, hl))
	}
	return result
}

func renderApplyPatchPreviewLine(line string, width int, hl *codeHighlighter) string {
	switch {
	case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
		code := line[1:]
		return "    " + DiffAddStyle.Render("+") + renderHighlightedSnippetLine(code, nil, max(width-1, 1), hl, diffAddBg)
	case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
		code := line[1:]
		return "    " + DiffDelStyle.Render("-") + renderHighlightedSnippetLine(code, nil, max(width-1, 1), hl, diffDelBg)
	case strings.HasPrefix(line, " "):
		return "    " + " " + renderHighlightedSnippetLine(line[1:], nil, max(width-1, 1), hl, "")
	case strings.HasPrefix(line, "@@"):
		return "    " + ToolResultExpandedStyle.Render(truncateApplyPatchDisplayLine(line, width))
	case strings.HasPrefix(line, "***"):
		return "    " + ToolResultStyle.Render(truncateApplyPatchDisplayLine(line, width))
	default:
		return "    " + DimStyle.Render(truncateApplyPatchDisplayLine(line, width))
	}
}

func truncateApplyPatchDisplayLine(line string, width int) string {
	if width <= 0 || tuiStringWidth(line) <= width {
		return line
	}
	if width == 1 {
		return "…"
	}
	return tuiCut(line, 0, width-1) + "…"
}

func applyPatchCodeSample(patch string) string {
	var lines []string
	for line := range strings.SplitSeq(patch, "\n") {
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") {
			lines = append(lines, line[1:])
		}
	}
	return strings.Join(lines, "\n")
}

func appendApplyPatchTargetLines(result []string, targets []tools.ApplyPatchDisplayTarget, width int) []string {
	// A single target is already shown in the tool-card header. Keep the
	// explicit list only when it adds information for a multi-file patch.
	if len(targets) <= 1 {
		return result
	}
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Targets:"))
	for _, target := range targets {
		marker, path := applyPatchTargetDisplay(target)
		result = append(result, "    "+DimStyle.Render(truncateApplyPatchDisplayLine(marker+" "+path, width)))
	}
	return result
}

func appendApplyPatchOperationSummaries(result []string, targets []tools.ApplyPatchDisplayTarget, width int) []string {
	if len(targets) <= 1 {
		return result
	}
	for _, target := range targets {
		marker, path := applyPatchTargetDisplay(target)
		if marker != "D" && (marker != "R" || target.Added != 0 || target.Removed != 0) {
			continue
		}
		line := truncateApplyPatchDisplayLine(marker+" "+path, width)
		result = append(result, ToolResultExpandedStyle.Render("  ↳ "+marker+" ")+DimStyle.Render(strings.TrimPrefix(line, marker+" ")))
	}
	return result
}

func applyPatchHasSummaryOnlyTargets(targets []tools.ApplyPatchDisplayTarget) bool {
	for _, target := range targets {
		marker, _ := applyPatchTargetDisplay(target)
		if marker == "D" || marker == "R" && target.Added == 0 && target.Removed == 0 {
			return true
		}
	}
	return false
}

func applyPatchOnlyMoveOrDeleteTargets(targets []tools.ApplyPatchDisplayTarget) bool {
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		marker, _ := applyPatchTargetDisplay(target)
		if marker != "D" && (marker != "R" || target.Added != 0 || target.Removed != 0) {
			return false
		}
	}
	return true
}

func applyPatchTargetDisplay(target tools.ApplyPatchDisplayTarget) (marker, path string) {
	marker, path = "M", target.SourcePath
	if target.TargetPath != "" {
		return "R", target.SourcePath + " → " + target.TargetPath
	}
	switch target.Kind {
	case tools.MutationAdd:
		marker = "A"
	case tools.MutationDelete:
		marker = "D"
	}
	return marker, path
}

func (b *Block) applyPatchTargets() []tools.ApplyPatchDisplayTarget {
	if b == nil || toolNameKey(b.ToolName) != tools.NameApplyPatch {
		return nil
	}
	targets, err := tools.ApplyPatchDisplayTargets(json.RawMessage(b.editPatchArgsJSON()))
	if err != nil {
		return nil
	}
	return targets
}

func appendReplaceEditPreview(result []string, args replaceEditArgs, filePath string, width int) []string {
	// Strip orphaned variation selectors before rendering so that width
	// measurement matches terminal zero-width rendering and the card
	// background fills completely.
	oldStr := tools.StripOrphanVariationSelectors(args.OldString)
	newStr := tools.StripOrphanVariationSelectors(args.NewString)
	hl := newCodeHighlighterWithLanguage(filePath, oldStr+"\n"+newStr, "")
	for _, line := range replaceEditPreviewLines(oldStr) {
		result = append(result, "    "+DiffDelStyle.Render("- ")+renderHighlightedSnippetLine(line, nil, max(width-2, 1), hl, diffDelBg))
	}
	for _, line := range replaceEditPreviewLines(newStr) {
		result = append(result, "    "+DiffAddStyle.Render("+ ")+renderHighlightedSnippetLine(line, nil, max(width-2, 1), hl, diffAddBg))
	}
	return result
}

func replaceEditPreviewLines(text string) []string {
	// Output is intentional full content: the preview mirrors exactly what the
	// tool applied. Do not truncate long new_string/old_string payloads.
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func (b *Block) editPatchArgsJSON() string {
	if strings.TrimSpace(b.RawArgs) != "" {
		return b.RawArgs
	}
	return b.Content
}

func renderEditPatchPreviewLine(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	styled := DimStyle.Render(line)
	switch {
	case strings.HasPrefix(trimmed, "+") && !strings.HasPrefix(trimmed, "+++"):
		styled = DiffAddStyle.Render(line)
	case strings.HasPrefix(trimmed, "-") && !strings.HasPrefix(trimmed, "---"):
		styled = DiffDelStyle.Render(line)
	case strings.HasPrefix(trimmed, "@@"):
		styled = ToolResultExpandedStyle.Render(line)
	case strings.HasPrefix(trimmed, "***"):
		styled = ToolResultStyle.Render(line)
	case strings.HasPrefix(trimmed, "..."):
		styled = DimStyle.Render(line)
	}
	return "    " + styled
}

func editPatchFromArgs(argsJSON string) string {
	var parsed struct {
		Patch string `json:"patch"`
	}
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Patch)
}

// applyPatchStreamingPreview best-effort extracts the patch text from
// still-streaming apply_patch args. Two shapes arrive here:
//
//   - JSON function shape: an incrementally received `{"patch":"..."}`
//     document that a full JSON parse cannot read until the stream closes, so
//     this locates the patch key (the document may be pretty-printed or carry
//     other keys first), decodes the escapes that have arrived so far, and
//     stops at the first unescaped closing quote.
//   - Freeform custom-tool shape: the args accumulate as bare patch text
//     (custom_tool_call_input.delta), which is shown as-is.
//
// It returns "" until recognizable text has streamed in, letting callers fall
// back to the path-only display.
func applyPatchStreamingPreview(argsJSON string) string {
	trimmed := strings.TrimSpace(argsJSON)
	if strings.HasPrefix(trimmed, "{") {
		seg, ok := streamingPatchValueSegment(trimmed)
		if !ok {
			return ""
		}
		return decodeStreamingPatchString(seg)
	}
	// Freeform custom-tool shape: bare patch text, no JSON envelope. Any
	// other payload (e.g. a gateway-lowered {"input":...} object) does not
	// start like a patch and stays hidden until args complete.
	if strings.HasPrefix(trimmed, "*** ") {
		if patch := sanitizeToolDisplayText(trimmed); patch != "" {
			return patch
		}
	}
	return ""
}

// streamingPatchValueSegment locates the value of the "patch" key in a
// still-streaming JSON object and returns the interior of its string value
// (after the opening quote). ok is false while the value has not started
// streaming yet or is not a string.
func streamingPatchValueSegment(argsJSON string) (string, bool) {
	for from := 0; ; {
		key := strings.Index(argsJSON[from:], `"patch"`)
		if key < 0 {
			return "", false
		}
		from += key + len(`"patch"`)
		seg := strings.TrimLeft(argsJSON[from:], " \t\r\n")
		if seg == "" {
			return "", false
		}
		if seg[0] != ':' {
			// The "patch" text appeared inside another string value; keep
			// scanning for the actual key.
			continue
		}
		seg = strings.TrimLeft(seg[1:], " \t\r\n")
		if seg == "" || seg[0] != '"' {
			// Not a string value (e.g. {"patch":123}); keep the caller's fallback.
			return "", false
		}
		return seg[1:], true
	}
}

// decodeStreamingPatchString decodes a still-streaming JSON string interior
// into patch text.
func decodeStreamingPatchString(seg string) string {
	var b strings.Builder
	b.Grow(len(seg))
	// JSON string escaping: \n / \t / \r / \" / \\ / \/ / \b / \f decode to
	// their literals and \uXXXX to the rune (surrogate pairs combine). A
	// trailing lone backslash is an incomplete escape.
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c == '"':
			// An unescaped quote closes the patch value (or an unterminated
			// tail); everything after it is not part of the patch.
			i = len(seg)
		case c == '\\':
			if i+1 >= len(seg) {
				i = len(seg)
				continue
			}
			i++
			switch seg[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'u':
				if n, ok := decodeJSONUnicodeEscape(seg, i); ok {
					if n >= 0xD800 && n <= 0xDBFF && i+11 <= len(seg) && seg[i+5] == '\\' && seg[i+6] == 'u' {
						if lo, loOK := decodeJSONUnicodeEscape(seg, i+6); loOK && lo >= 0xDC00 && lo <= 0xDFFF {
							b.WriteRune(rune(0x10000 + (n-0xD800)<<10 + (lo - 0xDC00)))
							i += 10
							continue
						}
					}
					if n < 0xD800 || n > 0xDFFF {
						b.WriteRune(rune(n))
						i += 4
						continue
					}
				}
				b.WriteString(`\u`)
			default:
				b.WriteByte('\\')
				b.WriteByte(seg[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	patch := strings.TrimSpace(b.String())
	if patch == "" {
		return ""
	}
	return sanitizeToolDisplayText(patch)
}

// decodeJSONUnicodeEscape decodes the 4 hex digits of the `\uXXXX` escape
// whose backslash sits at seg[i]. ok is false when fewer than 4 characters
// have streamed in or they are not hex.
func decodeJSONUnicodeEscape(seg string, i int) (rune, bool) {
	if i+5 > len(seg) {
		return 0, false
	}
	n, err := strconv.ParseUint(seg[i+1:i+5], 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(n), true
}

// replaceEditArgs holds the old_string/new_string replacement args of an Edit
// tool call.
type replaceEditArgs struct {
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll *bool  `json:"replace_all,omitempty"`
}

// parseReplaceEditArgs extracts the text-replacement args of an Edit tool call.
// ok is false when the args are not old_string/new_string replacements, letting
// callers fall back to the unified-diff representation.
func parseReplaceEditArgs(argsJSON string) (replaceEditArgs, bool) {
	var parsed replaceEditArgs
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil || parsed.OldString == "" {
		return replaceEditArgs{}, false
	}
	return parsed, true
}

func editPatchPreviewLines(patch string) []string {
	// Output is intentional full content: the request patch mirrors the diff
	// body the tool applies verbatim, so it must never be line-clipped. Long
	// lines are still width-wrapped at render time.
	return strings.Split(strings.TrimSpace(patch), "\n")
}

func (b *Block) diffToolFilePath() string {
	return b.diffToolFilePathWithTargets(b.applyPatchTargets())
}

// diffToolFilePathWithTargets is the allocation-conscious variant: callers
// that already parsed the apply_patch targets (renderFileDiffCall) pass them
// in so the args JSON is not parsed a second time in the same frame.
func (b *Block) diffToolFilePathWithTargets(targets []tools.ApplyPatchDisplayTarget) string {
	if toolNameKey(b.ToolName) == tools.NameApplyPatch {
		if len(targets) == 0 {
			var parsed struct {
				Paths []string `json:"paths"`
			}
			var paths []string
			if json.Unmarshal([]byte(b.Content), &parsed) == nil {
				paths = parsed.Paths
			}
			if len(paths) == 0 {
				paths = paramStringList(tolerantToolArgValue(b.Content, "paths"))
			}
			if len(paths) == 0 {
				// The card body may still carry the raw streamed patch (freeform
				// custom-tool input, or a JSON document that has not finished).
				// Derive the header target from the patch markers directly, so
				// the header shows a real path while the patch is still arriving.
				if patch := applyPatchStreamingPreview(b.Content); patch != "" {
					paths = streamingApplyPatchFilePaths(patch)
				}
			}
			if len(paths) == 0 {
				return ""
			}
			return applyPatchPathSummary(paths[0], len(paths))
		}
		marker, path := applyPatchTargetDisplay(targets[0])
		if marker == "D" {
			path = marker + " " + path
		}
		return applyPatchPathSummary(path, len(targets))
	}
	if b.ToolName == tools.NameEdit {
		path := tools.ExtractEditPathFromArgs(json.RawMessage(b.Content))
		if path == "" {
			path = strings.TrimSpace(tolerantToolArgValue(b.Content, "path"))
		}
		if path == "" {
			return ""
		}
		// ExtractEditPathFromArgs resolves to an absolute path. Shorten it to a
		// cwd-relative form so a long absolute prefix (deep tree, long $HOME,
		// worktree) does not push the file name out of the width-clipped header.
		// The caller additionally relativizes against displayWorkingDir.
		if rel := relToProcessWorkingDir(path); rel != "" {
			return rel
		}
		return path
	}
	return strings.TrimSpace(tolerantToolArgValue(b.Content, "path"))
}

// streamingApplyPatchFilePaths returns the model-facing file paths mentioned in
// still-streaming apply_patch text (freeform custom-tool input or an
// in-progress JSON document). It scans begin markers so a live patch card keeps
// a readable header target even when the strict ParseApplyPatch parser cannot
// yet read an incomplete document.
func streamingApplyPatchFilePaths(text string) []string {
	markers := []string{
		tools.ApplyPatchUpdateFileMarker,
		tools.ApplyPatchAddFileMarker,
		tools.ApplyPatchDeleteFileMarker,
		tools.ApplyPatchMoveToMarker,
	}
	var paths []string
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range markers {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				paths = append(paths, strings.TrimSpace(rest))
			}
		}
	}
	return paths
}

func applyPatchPathSummary(path string, count int) string {
	path = strings.TrimSpace(path)
	if count <= 1 {
		return path
	}
	return fmt.Sprintf("%s +%d files", path, count-1)
}

// processWorkingDir caches os.Getwd so per-render path shortening does not pay a
// syscall on every diff block.
var processWorkingDir = sync.OnceValue(func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
})

// relToProcessWorkingDir returns path made relative to the process working
// directory, or "" if it cannot be cleanly relativized (different root, escapes
// upward via "..", or already a usable relative path is not produced).
func relToProcessWorkingDir(path string) string {
	if !filepath.IsAbs(path) {
		return path
	}
	wd := processWorkingDir()
	if wd == "" {
		return ""
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return ""
	}
	rel = filepath.Clean(rel)
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	return rel
}
