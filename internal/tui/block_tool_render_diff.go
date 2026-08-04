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

// maxTUIDiffLines is the maximum number of diff lines rendered in the TUI.
const maxTUIDiffLines = 200

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

// appendApplyPatchToolUnifiedDiffPair renders one logical (-,+) line pair from a unified diff.
func appendApplyPatchToolUnifiedDiffPair(result *[]string, oldLine, newLine string, oldLineNum, newLineNum, diffWidth int, hl *codeHighlighter) int {
	formatLineNum := func(n int) string { return fmt.Sprintf("%4d ", n) }
	if lines := renderInlineDiffLine(oldLine, newLine, diffWidth, hl); lines != nil {
		if strings.HasPrefix(lines[0], "+") {
			*result = append(*result, "  "+DimStyle.Render(formatLineNum(newLineNum))+lines[0])
		} else {
			*result = append(*result, "  "+DimStyle.Render(formatLineNum(oldLineNum))+lines[0])
		}
		return 1
	}
	oldSegs, newSegs := tools.InlineDiff(oldLine, newLine)
	oldCode := renderHighlightedSnippetLine(oldLine, filterDiffSpansByKind(buildDiffSegmentSpans(oldSegs), "delete"), diffWidth-1, hl, diffDelBg)
	newCode := renderHighlightedSnippetLine(newLine, filterDiffSpansByKind(buildDiffSegmentSpans(newSegs), "insert"), diffWidth-1, hl, diffAddBg)
	*result = append(*result,
		"  "+DimStyle.Render(formatLineNum(oldLineNum))+DiffDelStyle.Render("-")+oldCode,
		"  "+DimStyle.Render(formatLineNum(newLineNum))+DiffAddStyle.Render("+")+newCode,
	)
	return 2
}

// renderFileDiffCall renders an Edit tool call with a unified diff view.
func (b *Block) renderFileDiffCall(width int, spinnerFrame string) []string {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	// Parse the apply_patch targets once per render; both the header path and
	// the body below need them, and parsing re-reads the patch args JSON.
	applyPatchTargets := b.applyPatchTargets()
	filePath := b.diffToolFilePathWithTargets(applyPatchTargets)
	if filePath != "" {
		filePath = b.displayToolPath(filePath)
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := renderToolHeaderLine(prefix, b.ToolName)
	if filePath != "" {
		headerLine += " " + DimStyle.Render(filePath)
	}
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())
	result = append(result, headerLine)
	if b.Collapsed {
		if strings.TrimSpace(b.Diff) == "" && strings.TrimSpace(b.ResultContent) != "" {
			displayResult := sanitizeToolDisplayText(toolCollapsedResultContent(b.ToolName, toolDisplayResultContent(b)))
			lineCount := len(strings.Split(displayResult, "\n"))
			summary := truncateOneLine(displayResult, cardWidth-26)
			if b.toolResultIsError() {
				result = append(result, ErrorStyle.Render(fmt.Sprintf("  ▸ ↳ %s (%d lines)", summary, lineCount)))
			} else if b.toolResultIsCancelled() {
				result = append(result, DimStyle.Render(fmt.Sprintf("  ▸ ↳ cancelled (%d lines)", lineCount)))
			} else {
				result = append(result, ToolResultStyle.Render(fmt.Sprintf("  ▸ ↳ %s (%d lines)", summary, lineCount)))
			}
		}
		return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
	}
	diffLines := strings.Split(b.Diff, "\n")
	multiFileApplyPatchDiff := b.ToolName == tools.NameApplyPatch && unifiedDiffFileCount(diffLines) > 1
	if b.ToolName == tools.NameApplyPatch {
		if !multiFileApplyPatchDiff {
			result = appendApplyPatchTargetLines(result, applyPatchTargets, cardWidth-4)
		}
		if strings.TrimSpace(b.Diff) == "" && !b.toolResultIsError() && !b.toolResultIsCancelled() {
			result = appendApplyPatchPreview(result, b.editPatchArgsJSON(), filePath, cardWidth-4)
		}
	}
	const diffLineNumWidth = 5
	diffWidth := max(cardWidth-4-diffLineNumWidth, 10)
	// Sample the diff content once; the initial highlighter and every per-file
	// section highlighter of a multi-file patch share the same sample.
	diffSample := diffContentSample(b.Diff)
	hl := ensureCodeHighlighter(&b.codeHL, filePath, diffSample)
	shownLines := 0
	seenHunk := false
	diffFileCount := 0
	var oldLineNum, newLineNum int
	formatLineNum := func(n int) string { return fmt.Sprintf("%4d ", n) }
	if !b.toolResultIsCancelled() {
	diffLoop:
		for i := 0; i < len(diffLines); i++ {
			line := diffLines[i]
			if line == "" {
				continue
			}
			if shownLines >= maxTUIDiffLines {
				result = append(result, "  "+DimStyle.Render("... (diff truncated)"))
				break
			}
			var rendered string
			switch {
			case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
				if i+1 < len(diffLines) {
					next := diffLines[i+1]
					if strings.HasPrefix(next, "-") && !strings.HasPrefix(next, "---") {
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
						if len(delBodies) >= 2 && len(delBodies) == len(addBodies) {
							for k := range delBodies {
								if shownLines >= maxTUIDiffLines {
									result = append(result, "  "+DimStyle.Render("... (diff truncated)"))
									break diffLoop
								}
								shownLines += appendApplyPatchToolUnifiedDiffPair(&result, delBodies[k], addBodies[k], oldLineNum, newLineNum, diffWidth, hl)
								oldLineNum++
								newLineNum++
							}
							i = addJ - 1
							continue
						}
					}
				}
				if i+1 < len(diffLines) {
					next := diffLines[i+1]
					if strings.HasPrefix(next, "+") && !strings.HasPrefix(next, "+++") {
						if shownLines >= maxTUIDiffLines {
							result = append(result, "  "+DimStyle.Render("... (diff truncated)"))
							break diffLoop
						}
						shownLines += appendApplyPatchToolUnifiedDiffPair(&result, line[1:], next[1:], oldLineNum, newLineNum, diffWidth, hl)
						oldLineNum++
						newLineNum++
						i++
						continue
					}
				}
				code := renderHighlightedSnippetLine(line[1:], []diffSegmentSpan{{StartCol: 0, EndCol: diffTextWidth(line[1:])}}, diffWidth-1, hl, diffDelBg)
				rendered = DimStyle.Render(formatLineNum(oldLineNum)) + DiffDelStyle.Render("-") + code
				oldLineNum++
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				code := renderHighlightedSnippetLine(line[1:], []diffSegmentSpan{{StartCol: 0, EndCol: diffTextWidth(line[1:])}}, diffWidth-1, hl, diffAddBg)
				rendered = DimStyle.Render(formatLineNum(newLineNum)) + DiffAddStyle.Render("+") + code
				newLineNum++
			case strings.HasPrefix(line, "@@"):
				if seenHunk {
					sep := DimStyle.Render("  ─────────────")
					result = append(result, "  "+sep)
					shownLines++
					if shownLines >= maxTUIDiffLines {
						result = append(result, "  "+DimStyle.Render("... (diff truncated)"))
						break diffLoop
					}
				}
				seenHunk = true
				hunkLine, _, _ := strings.Cut(line, "\n")
				if m := diffHunkHeaderRe.FindStringSubmatch(hunkLine); len(m) == 5 {
					oldStart, _ := strconv.Atoi(m[1])
					newStart, _ := strconv.Atoi(m[3])
					oldLineNum, newLineNum = oldStart, newStart
				}
				continue
			case strings.HasPrefix(line, "--- "):
				if multiFileApplyPatchDiff && i+1 < len(diffLines) && strings.HasPrefix(diffLines[i+1], "+++ ") {
					marker, path, syntaxPath := b.applyPatchDiffSectionDisplay(applyPatchTargets, line, diffLines[i+1])
					if diffFileCount > 0 {
						result = append(result, "  "+DimStyle.Render("─────────────"))
						shownLines++
					}
					result = append(result, ToolResultExpandedStyle.Render("  ↳ "+marker+" ")+DimStyle.Render(path))
					shownLines++
					seenHunk = false
					oldLineNum, newLineNum = 0, 0
					hl = newCodeHighlighterWithLanguage(syntaxPath, diffSample, "")
					diffFileCount++
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
				code := renderHighlightedSnippetLine(content, nil, diffWidth-1, hl, "")
				displayLineNum := max(newLineNum, oldLineNum)
				rendered = DimStyle.Render(formatLineNum(displayLineNum)) + " " + code
				oldLineNum++
				newLineNum++
			}
			result = append(result, "  "+rendered)
			shownLines++
		}
	}
	if (b.ToolName == tools.NameEdit || b.ToolName == tools.NameApplyPatch) && strings.TrimSpace(b.ResultContent) != "" && !b.toolResultIsError() && !b.toolResultIsCancelled() && !toolShouldHideSuccessfulFileOpResult(b) {
		result = append(result, ToolResultExpandedStyle.Render("  ↳ Diagnostics:"))
		result = append(result, renderLSPDiagnosticsLines(editSuccessDiagnosticsContent(b.ResultContent), "    ", cardWidth-4)...)
	}
	if b.toolResultIsError() && b.ResultContent != "" {
		if b.ToolName == tools.NameApplyPatch {
			result = appendApplyPatchPreview(result, b.editPatchArgsJSON(), filePath, cardWidth-4)
		} else if b.ToolName == tools.NameEdit {
			before := len(result)
			result = appendEditPatchPreview(result, b.editPatchArgsJSON(), cardWidth-4)
			if len(result) == before {
				result = appendReplaceEditPreview(result, b.editPatchArgsJSON(), cardWidth-4)
			}
		}
		result = append(result, ErrorStyle.Render("  ↳ Error:"))
		result = append(result, renderLSPDiagnosticsLines(toolErrorDisplayContent(b.ResultContent), "    ", cardWidth-4)...)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = append(result, DimStyle.Render("  ↳ Cancelled"))
		if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
			result = append(result, renderLSPDiagnosticsLines(detail, "    ", cardWidth-4)...)
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.displayLabelID()), result, toolCardBg, railANSISeq("tool", b.Focused))
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

func (b *Block) applyPatchDiffSectionDisplay(targets []tools.ApplyPatchDisplayTarget, oldHeader, newHeader string) (marker, path, syntaxPath string) {
	oldPath := strings.TrimSpace(strings.TrimPrefix(oldHeader, "--- "))
	newPath := strings.TrimSpace(strings.TrimPrefix(newHeader, "+++ "))
	for _, target := range targets {
		if oldPath != target.SourcePath && newPath != target.SourcePath &&
			oldPath != target.TargetPath && newPath != target.TargetPath {
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
	patch := editPatchFromArgs(argsJSON)
	if patch == "" {
		return result
	}
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Patch:"))
	for _, line := range editPatchPreviewLines(patch) {
		for _, wrapped := range wrapIndentedText(line, width) {
			result = append(result, renderEditPatchPreviewLine(wrapped))
		}
	}
	return result
}

func appendApplyPatchPreview(result []string, argsJSON, filePath string, width int) []string {
	patch := editPatchFromArgs(argsJSON)
	if patch == "" {
		return result
	}
	hl := newCodeHighlighterWithLanguage(filePath, applyPatchCodeSample(patch), "")
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Patch:"))
	for _, line := range editPatchPreviewLines(patch) {
		for _, wrapped := range wrapIndentedText(line, width) {
			result = append(result, renderApplyPatchPreviewLine(wrapped, width, hl))
		}
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
		return "    " + ToolResultExpandedStyle.Render(line)
	case strings.HasPrefix(line, "***"):
		return "    " + ToolResultStyle.Render(line)
	default:
		return "    " + DimStyle.Render(line)
	}
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
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Files:"))
	for _, target := range targets {
		marker, path := applyPatchTargetDisplay(target)
		for _, wrapped := range wrapIndentedText(marker+" "+path, width) {
			result = append(result, "    "+DimStyle.Render(wrapped))
		}
	}
	return result
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

func appendReplaceEditPreview(result []string, argsJSON string, width int) []string {
	preview := replaceEditPreviewFromArgs(argsJSON)
	if preview == "" {
		return result
	}
	result = append(result, ToolResultExpandedStyle.Render("  ↳ Arguments:"))
	for _, line := range editPatchPreviewLines(preview) {
		for _, wrapped := range wrapIndentedText(line, width) {
			result = append(result, "    "+DimStyle.Render(wrapped))
		}
	}
	return result
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

func replaceEditPreviewFromArgs(argsJSON string) string {
	var parsed struct {
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll *bool  `json:"replace_all,omitempty"`
	}
	if json.Unmarshal([]byte(argsJSON), &parsed) != nil || parsed.OldString == "" {
		return ""
	}
	preview := map[string]any{
		"old_string": parsed.OldString,
		"new_string": parsed.NewString,
	}
	if parsed.ReplaceAll != nil {
		preview["replace_all"] = *parsed.ReplaceAll
	}
	formatted, err := json.MarshalIndent(preview, "", "  ")
	if err != nil {
		return ""
	}
	return string(formatted)
}

func editPatchPreviewLines(patch string) []string {
	lines := strings.Split(strings.TrimSpace(patch), "\n")
	if len(lines) > 20 {
		lines = append(lines[:20], "... (patch truncated)")
	}
	return lines
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
			if json.Unmarshal([]byte(b.Content), &parsed) != nil || len(parsed.Paths) == 0 {
				return ""
			}
			return applyPatchPathSummary(parsed.Paths[0], len(parsed.Paths))
		}
		_, path := applyPatchTargetDisplay(targets[0])
		return applyPatchPathSummary(path, len(targets))
	}
	if b.ToolName == tools.NameEdit {
		path := tools.ExtractEditPathFromArgs(json.RawMessage(b.Content))
		if path == "" {
			var parsed struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(b.Content), &parsed) == nil {
				path = strings.TrimSpace(parsed.Path)
			}
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
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(b.Content), &parsed) != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Path)
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
