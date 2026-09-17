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
	"github.com/keakon/chord/internal/tui/markdownutil"
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
	// A failed apply_patch still commits the groups that planned cleanly. The
	// result text names them ("Applied patch:") and lists the rest ("Not
	// applied:"), so the card can mark each file with ✓/✗ without touching the
	// model-facing text. Parse the sections once — the diff section headers
	// below and the error body both read them.
	applyPatchError := b.ToolName == tools.NameApplyPatch && b.toolResultIsError()
	var applyPatchSections applyPatchErrorSections
	if applyPatchError {
		applyPatchSections = splitApplyPatchErrorSections(b.stripResultNotes(b.ResultContent))
	}
	// filePath is a header display summary ("a → b", "D path", "path +N files"),
	// which is not a path; syntax highlighting needs the undecorated target so
	// lexerForFilePath can resolve the real lexer.
	filePath, syntaxPath := b.diffToolPathsWithTargets(applyPatchTargets)
	if filePath != "" {
		filePath = b.displayToolPath(filePath)
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	// The card surface spans the full viewport, but wrapped plain-text error /
	// diagnostic sections keep a readable column: they wrap at the prose cap
	// instead of becoming ultra-wide lines (see
	// appendApplyPatchErrorTextLines / renderLSPDiagnosticsLines). Line-oriented
	// content (diffs, previews, target summaries) deliberately uses the full
	// cardWidth-4 instead, clipping per line so more file content shows.
	textWrap := max(min(cardWidth-4, maxProseWidth), 10)
	// Edit and apply_patch cards are always expanded, so the header carries the
	// +/- summary and no disclosure marker is needed.
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
	diffLines := strings.Split(displayDiff, "\n")
	diffFileCount := unifiedDiffFileCount(diffLines)
	// An errored card groups per file even for a single diff section: the
	// section header is where the ✓ marking that file as already on disk lives.
	groupedApplyPatchDiff := b.ToolName == tools.NameApplyPatch &&
		(diffFileCount > 1 || hasOperationSummaries && diffFileCount > 0 || applyPatchError && diffFileCount > 0)
	if b.ToolName == tools.NameApplyPatch {
		// On an errored card the args-derived target list and the summary-only
		// rows carry no status: they would draw failed files unmarked right
		// next to the ✓/✗ marks, so the per-file sections and the result text
		// carry the whole status picture instead.
		if !applyPatchError {
			if hasOperationSummaries {
				result = appendApplyPatchOperationSummaries(result, applyPatchTargets, cardWidth-4)
			} else if !groupedApplyPatchDiff {
				result = appendApplyPatchTargetLines(result, applyPatchTargets, cardWidth-4)
			}
		}
		if strings.TrimSpace(displayDiff) == "" && !applyPatchNoChanges && !b.toolResultIsError() && !b.toolResultIsCancelled() &&
			!applyPatchOnlyMoveOrDeleteTargets(applyPatchTargets) {
			result = appendApplyPatchPreview(result, b, syntaxPath, cardWidth-4)
		}
		if applyPatchNoChanges {
			result = append(result, toolFieldStandalone(DimStyle, "No changes"))
		}
		if strings.TrimSpace(displayDiff) != "" && b.toolResultIsError() {
			result = append(result, toolFieldSection(ToolResultExpandedStyle, "Applied changes"))
		}
	}
	const diffLineNumWidth = 5
	diffWidth := max(cardWidth-4-diffLineNumWidth, 10)
	// Sample the diff content once; the initial highlighter and every per-file
	// section highlighter of a multi-file patch share the same sample.
	diffSample := diffContentSample(displayDiff)
	hl := ensureCodeHighlighter(&b.codeHL, syntaxPath, diffSample)
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
					result = append(result, applyPatchDiffSeparator(cardWidth-4))
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
						result = append(result, applyPatchDiffSeparator(cardWidth-4))
					}
					// Every diff section on an errored card is a group that
					// already landed on disk, so its header carries the same ✓
					// the "Applied patch:" list would.
					mark := ""
					if applyPatchError {
						mark = "✓ "
					}
					filePrefix := "  ↳ " + mark + marker + " "
					fileLine := truncateApplyPatchDisplayLine(filePrefix+path, cardWidth)
					result = append(result, applyPatchDiffSectionPrefix(mark, marker)+DimStyle.Render(strings.TrimPrefix(fileLine, filePrefix)))
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
		result = append(result, toolFieldSection(ToolResultExpandedStyle, "Diagnostics"))
		result = append(result, renderLSPDiagnosticsLines(editSuccessDiagnosticsContent(b.stripResultNotes(b.ResultContent)), "    ", textWrap)...)
	}
	// A successful edit shows the change it applied. When the diff is missing —
	// a durable checkpoint elides it together with the rest of the body, the
	// diff was never persisted, or it could not be generated — the requested
	// replacement still sits in the args, so the card falls back to the same
	// preview a failed edit renders instead of losing what changed.
	if b.ResultDone && b.ToolName == tools.NameEdit && !b.toolResultIsError() && !b.toolResultIsCancelled() && strings.TrimSpace(displayDiff) == "" {
		result = appendEditArgsPreview(result, b, replaceArgs, hasReplaceArgs, syntaxPath, cardWidth-4)
	}
	if b.toolResultIsError() && b.ResultContent != "" {
		switch b.ToolName {
		case tools.NameApplyPatch:
			if strings.TrimSpace(displayDiff) == "" {
				result = appendApplyPatchPreview(result, b, syntaxPath, cardWidth-4)
				if applyPatchSections.applied != "" {
					result = append(result, toolFieldSection(ToolResultExpandedStyle, "Applied changes"))
					result = appendApplyPatchErrorTextLines(result, applyPatchSections.applied, textWrap)
				}
			}
			result = append(result, toolFieldSection(ErrorStyle, "Error"))
			result = appendApplyPatchErrorTextLines(result, applyPatchSections.failure, textWrap)
			if applyPatchSections.diagnostics != "" {
				result = append(result, toolFieldSection(ToolResultExpandedStyle, "Diagnostics"))
				result = append(result, renderLSPDiagnosticsLines(applyPatchSections.diagnostics, "    ", textWrap)...)
			}
		case tools.NameEdit:
			if strings.TrimSpace(displayDiff) == "" {
				result = appendEditArgsPreview(result, b, replaceArgs, hasReplaceArgs, syntaxPath, cardWidth-4)
			}
			result = append(result, toolFieldSection(ErrorStyle, "Error"))
			result = append(result, renderLSPDiagnosticsLines(toolErrorDisplayContent(b.stripResultNotes(b.ResultContent)), "    ", textWrap)...)
		}
	} else if b.toolResultIsCancelled() {
		appendToolOutcomeBody(&result, toolOutcomeCancelled, toolDisplayResultContent(b), textWrap, true)
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
	content = strings.TrimSpace(markdownutil.NormalizeNewlines(content))
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
//
// Each line also gets the status its own prefix implies, so a partially applied
// patch reads at a glance instead of making the user compare the two lists: a
// failed file group ("- path: reason") is marked ✗, and a committed file from
// the "Applied patch:" list ("M path") is marked ✓.
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
		mark, markStyle := "", ToolResultExpandedStyle
		switch {
		case strings.HasPrefix(displayLine, "- "):
			mark, markStyle = "✗", ToolStatusErrorStyle
			displayLine = strings.TrimPrefix(displayLine, "- ")
		case strings.HasPrefix(displayLine, "A "), strings.HasPrefix(displayLine, "M "),
			strings.HasPrefix(displayLine, "D "), strings.HasPrefix(displayLine, "R "):
			mark, markStyle = "✓", ToolStatusSuccessStyle
		}
		if mark == "" {
			for _, wl := range wrapText(displayIndent+displayLine, width) {
				result = append(result, ToolResultExpandedStyle.Render(wl))
			}
			continue
		}
		// The mark joins the wrapped text so it consumes display width like any
		// other rune and continuation lines still align under the body; only the
		// glyph on the first line is coloured.
		for i, wl := range wrapText(displayIndent+mark+" "+displayLine, width) {
			after, ok := strings.CutPrefix(wl, displayIndent+mark)
			if i != 0 || !ok {
				result = append(result, ToolResultExpandedStyle.Render(wl))
				continue
			}
			result = append(result, ToolResultExpandedStyle.Render(displayIndent)+markStyle.Render(mark)+ToolResultExpandedStyle.Render(after))
		}
	}
	return result
}

// applyPatchDiffSectionPrefix styles the "  ↳ ✓ M " lead of a per-file diff
// section header. mark is "" for a successful card; on an errored card it holds
// the ✓ marking the file's changes as already on disk, which keeps the status
// colour used by the other ✓/✗ marks.
func applyPatchDiffSectionPrefix(mark, marker string) string {
	if mark == "" {
		return ToolResultExpandedStyle.Render("  ↳ " + marker + " ")
	}
	return ToolResultExpandedStyle.Render("  ↳ ") +
		ToolStatusSuccessStyle.Render(strings.TrimSuffix(mark, " ")) +
		ToolResultExpandedStyle.Render(" "+marker+" ")
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

// appendEditArgsPreview renders the requested edit when no diff is available,
// preferring the replace args preview and falling back to the patch preview.
func appendEditArgsPreview(result []string, b *Block, replaceArgs replaceEditArgs, hasReplaceArgs bool, syntaxPath string, width int) []string {
	if hasReplaceArgs {
		return appendReplaceEditPreview(result, replaceArgs, syntaxPath, width)
	}
	return appendEditPatchPreview(result, b.editPatchArgsJSON(), width)
}

func appendEditPatchPreview(result []string, argsJSON string, width int) []string {
	patch := tools.StripOrphanVariationSelectors(editPatchFromArgs(argsJSON))
	if patch == "" {
		return result
	}
	result = append(result, toolFieldSection(ToolResultExpandedStyle, "Patch"))
	// Truncate (not wrap) each patch line, matching the apply_patch preview and
	// the diff body. Diffs/file content are column-aligned; wrapping breaks the
	// +/- gutter alignment and is harder to read than a clipped line.
	for _, line := range editPatchPreviewLines(patch) {
		result = append(result, renderEditPatchPreviewLine(truncateApplyPatchDisplayLine(line, width)))
	}
	return result
}

const (
	// patchPreviewCoalesceBytes is the minimum growth between preview
	// refreshes while the patch is still small enough that a refresh is cheap.
	patchPreviewCoalesceBytes = 256
	// patchPreviewCoalesceGrowthFrom is the accumulated-args size past which a
	// refresh stops being cheap: every refresh re-measures the whole card, so a
	// fixed window costs one linear pass per window and the stream is
	// quadratic in the patch. Past this point the window grows with the patch,
	// which makes the refresh count logarithmic. Small patches keep the tight
	// window so the preview still tracks the stream closely.
	patchPreviewCoalesceGrowthFrom = 16 << 10
	// patchPreviewCoalesceDivisor sets how fast the grown window widens: one
	// refresh per 1/divisor of the accumulated args.
	patchPreviewCoalesceDivisor = 16
)

// cachedApplyPatchStreamingArgs mirrors streamingToolDisplayArgs for a live
// apply_patch card. Extracting the streaming preview is linear in the
// accumulated args, so re-running it per streamed fragment would be
// quadratic; the preview refreshes at most once per coalesce window of growth
// instead (see patchPreviewCoalesceWindow). Complete-JSON and path-only
// displays are stable or cheap and re-evaluate each call.
func (b *Block) cachedApplyPatchStreamingArgs(argsJSON string) string {
	if b.patchPreviewText != "" && len(argsJSON) >= b.patchPreviewLen &&
		len(argsJSON)-b.patchPreviewLen < b.patchPreviewCoalesceWindow() {
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

// clearApplyPatchPreviewMemo drops the rendered-line memo used only while the
// patch is still streaming, so a finished card does not retain it.
func (b *Block) clearApplyPatchPreviewMemo() {
	b.previewRenderedPatch = ""
	b.previewRenderedWidth = 0
	b.previewRenderedHL = nil
	b.previewRenderedHLPath = ""
	b.previewRenderedLines = nil
	b.clearStreamingApplyPatchRangeMemo()
}

// patchPreviewCoalesceWindow returns how much the accumulated args must grow
// before the live preview refreshes again. It is the tight
// patchPreviewCoalesceBytes window until the args pass
// patchPreviewCoalesceGrowthFrom, then widens in proportion to them.
func (b *Block) patchPreviewCoalesceWindow() int {
	if b.patchPreviewLen <= patchPreviewCoalesceGrowthFrom {
		return patchPreviewCoalesceBytes
	}
	return b.patchPreviewLen / patchPreviewCoalesceDivisor
}

func appendApplyPatchPreview(result []string, b *Block, syntaxPath string, width int) []string {
	argsJSON := b.editPatchArgsJSON()
	// Sanitize the display copy before splitting it into lines. In particular,
	// a CR left inside a patch line would be interpreted by the screen renderer
	// as a cursor return and could overwrite the rail and card padding. Keep the
	// raw argument untouched: this is only a presentation boundary.
	patch := sanitizeToolDisplayText(editPatchFromArgs(argsJSON))
	if patch == "" {
		// Args may still be streaming: the JSON is not parseable yet, but the
		// patch text itself is a valid live preview (see
		// applyPatchStreamingPreview).
		patch = sanitizeToolDisplayText(applyPatchStreamingPreview(argsJSON))
	}
	if patch == "" {
		return result
	}
	hl := b.applyPatchPreviewHighlighter(syntaxPath, patch)
	result = append(result, toolFieldSection(ToolResultExpandedStyle, "Requested patch"))
	result = append(result, b.appendApplyPatchPreviewLines(patch, width, hl)...)
	return result
}

// applyPatchPreviewHighlighter returns the block's preview highlighter,
// building the lexer-detection sample only while the highlighter still needs
// it. The sample is a linear function of the patch and the preview is rebuilt
// on every coalesced stream step, so skipping it once the lexer is resolved
// keeps each step's cost proportional to the newly streamed text.
func (b *Block) applyPatchPreviewHighlighter(filePath, patch string) *codeHighlighter {
	if h := b.previewHL; h != nil && h.lexerResolved && h.filePath == filePath {
		return h
	}
	return ensureCodeHighlighterWithLanguage(&b.previewHL, filePath, applyPatchCodeSample(patch), "")
}

// appendApplyPatchPreviewLines renders the patch preview lines, reusing the
// previously rendered prefix when the patch only grew and the same highlighter
// state still applies. The last memoized line is always re-rendered because the
// stream may still be extending it.
//
// hl is compared by identity and by the file path it is currently resolved for:
// a multi-file patch (or a first coalesced frame whose path has not been parsed
// out yet) swaps the lexer in place mid-stream, and reusing lines highlighted by
// the previous lexer would leave the preview in two colour schemes for the rest
// of the stream. Dropping the memo re-renders every line once with the new
// lexer instead.
func (b *Block) appendApplyPatchPreviewLines(patch string, width int, hl *codeHighlighter) []string {
	lines := editPatchPreviewLines(patch)
	hlPath := ""
	if hl != nil {
		hlPath = hl.filePath
	}
	reuse := 0
	if b.previewRenderedWidth == width && b.previewRenderedPatch != "" &&
		b.previewRenderedHL == hl && b.previewRenderedHLPath == hlPath &&
		len(b.previewRenderedLines) > 0 && len(lines) >= len(b.previewRenderedLines) &&
		strings.HasPrefix(patch, b.previewRenderedPatch) {
		reuse = len(b.previewRenderedLines) - 1
	}
	out := make([]string, 0, len(lines))
	if reuse > 0 {
		out = append(out, b.previewRenderedLines[:reuse]...)
	}
	for _, line := range lines[reuse:] {
		out = append(out, renderApplyPatchPreviewLine(line, width, hl))
	}
	b.previewRenderedPatch = patch
	b.previewRenderedWidth = width
	b.previewRenderedHL = hl
	b.previewRenderedHLPath = hlPath
	b.previewRenderedLines = out
	return out
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
		return renderApplyPatchPreviewHunkHeader(line, width)
	case strings.HasPrefix(line, "***"):
		return "    " + ToolResultStyle.Render(truncateApplyPatchDisplayLine(line, width))
	default:
		return "    " + DimStyle.Render(truncateApplyPatchDisplayLine(line, width))
	}
}

func renderApplyPatchPreviewHunkHeader(line string, width int) string {
	header := truncateApplyPatchDisplayLine(line, width)
	headerWidth := tuiStringWidth(header)
	if headerWidth >= width {
		return "    " + ToolResultExpandedStyle.Render(header)
	}

	// Keep the hunk separator on the header row so the line-level render memo
	// stays one-to-one with the patch input. The body remains independently
	// highlighted; this is only a visual boundary, not a new syntax block.
	// Standalone diff separators use applyPatchDiffSeparator with the same
	// full-width dim rule so requested-patch and applied-diff views share one
	// visual language.
	ruleWidth := width - headerWidth
	rule := strings.Repeat("─", ruleWidth)
	return "    " + ToolResultExpandedStyle.Render(header) + DimStyle.Render(rule)
}

// applyPatchDiffSeparator renders a standalone full-width dim rule for applied
// diff hunk/file boundaries, matching the inline rule in
// renderApplyPatchPreviewHunkHeader. Width is the content width (cardWidth-4).
func applyPatchDiffSeparator(width int) string {
	return "    " + DimStyle.Render(strings.Repeat("─", max(width, 1)))
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
	result = append(result, toolFieldSection(ToolResultExpandedStyle, "Targets"))
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
		result = append(result, toolFieldMarker(ToolResultExpandedStyle, marker+" ")+DimStyle.Render(strings.TrimPrefix(line, marker+" ")))
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

// diffToolFilePathWithTargets is the display-only view used by headers, copy
// and selection output; see diffToolPathsWithTargets for the lexer seed.
func (b *Block) diffToolFilePathWithTargets(targets []tools.ApplyPatchDisplayTarget) string {
	displayPath, _ := b.diffToolPathsWithTargets(targets)
	return displayPath
}

// diffToolPathsWithTargets is the allocation-conscious variant: callers that
// already parsed the apply_patch targets (renderFileDiffCall, the streaming card
// layout) pass them in so the args JSON is not parsed a second time in the same
// frame. It resolves the header display path and the undecorated syntax path in
// one pass: the display form ("a → b", "D path", "path +N files") is not a path,
// and lexerForFilePath resolves any of those to no lexer at all, which silently
// turns a card's preview into plain text.
func (b *Block) diffToolPathsWithTargets(targets []tools.ApplyPatchDisplayTarget) (displayPath, syntaxPath string) {
	if toolNameKey(b.ToolName) == tools.NameApplyPatch {
		if len(targets) == 0 {
			paths := b.unparsedApplyPatchPaths()
			if len(paths) == 0 {
				return "", ""
			}
			return applyPatchPathSummary(paths[0], len(paths)), strings.TrimSpace(paths[0])
		}
		syntaxPath = applyPatchTargetSyntaxPath(targets[0])
		marker, path := applyPatchTargetDisplay(targets[0])
		if marker == "D" {
			path = marker + " " + path
		}
		return applyPatchPathSummary(path, len(targets)), syntaxPath
	}
	if b.ToolName == tools.NameEdit {
		path := tools.ExtractEditPathFromArgs(json.RawMessage(b.Content))
		if path == "" {
			path = strings.TrimSpace(tolerantToolArgValue(b.Content, "path"))
		}
		if path == "" {
			return "", ""
		}
		// ExtractEditPathFromArgs resolves to an absolute path. Shorten it to a
		// cwd-relative form so a long absolute prefix (deep tree, long $HOME,
		// worktree) does not push the file name out of the width-clipped header.
		// The caller additionally relativizes against displayWorkingDir.
		if rel := relToProcessWorkingDir(path); rel != "" {
			return rel, rel
		}
		return path, path
	}
	path := strings.TrimSpace(tolerantToolArgValue(b.Content, "path"))
	return path, path
}

// unparsedApplyPatchPaths resolves the file paths visible while the args cannot
// yet be read as a complete apply_patch document: the explicit "paths" argument
// when present, otherwise the markers in the still-streaming patch text.
func (b *Block) unparsedApplyPatchPaths() []string {
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
		// custom-tool input, or a JSON document that has not finished). Derive
		// the header target from the patch markers directly, so the header shows
		// a real path while the patch is still arriving.
		if patch := applyPatchStreamingPreview(b.Content); patch != "" {
			paths = streamingApplyPatchFilePaths(patch)
		}
	}
	return paths
}

// applyPatchTargetSyntaxPath picks the path whose content the diff shows: the
// destination of a move or rename, otherwise the source file.
func applyPatchTargetSyntaxPath(target tools.ApplyPatchDisplayTarget) string {
	if target.TargetPath != "" {
		return target.TargetPath
	}
	return target.SourcePath
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
