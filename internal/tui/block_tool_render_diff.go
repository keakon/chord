package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

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

// appendPatchToolUnifiedDiffPair renders one logical (-,+) line pair from a unified diff.
func appendPatchToolUnifiedDiffPair(result *[]string, oldLine, newLine string, oldLineNum, newLineNum, diffWidth int, hl *codeHighlighter) int {
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
	filePath := b.diffToolFilePath()
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
		return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.ID), result, toolCardBg, railANSISeq("tool", b.Focused))
	}
	const diffLineNumWidth = 5
	diffWidth := cardWidth - 4 - diffLineNumWidth
	if diffWidth < 10 {
		diffWidth = 10
	}
	hl := ensureCodeHighlighter(&b.codeHL, filePath, diffContentSample(b.Diff))
	diffLines := strings.Split(b.Diff, "\n")
	shownLines := 0
	seenHunk := false
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
								shownLines += appendPatchToolUnifiedDiffPair(&result, delBodies[k], addBodies[k], oldLineNum, newLineNum, diffWidth, hl)
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
						shownLines += appendPatchToolUnifiedDiffPair(&result, line[1:], next[1:], oldLineNum, newLineNum, diffWidth, hl)
						oldLineNum++
						newLineNum++
						i++
						continue
					}
				}
				code := renderHighlightedSnippetLine(line[1:], []diffSegmentSpan{{StartCol: 0, EndCol: ansi.StringWidth(line[1:])}}, diffWidth-1, hl, diffDelBg)
				rendered = DimStyle.Render(formatLineNum(oldLineNum)) + DiffDelStyle.Render("-") + code
				oldLineNum++
			case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
				code := renderHighlightedSnippetLine(line[1:], []diffSegmentSpan{{StartCol: 0, EndCol: ansi.StringWidth(line[1:])}}, diffWidth-1, hl, diffAddBg)
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
				hunkLine := strings.SplitN(line, "\n", 2)[0]
				if m := diffHunkHeaderRe.FindStringSubmatch(hunkLine); len(m) == 5 {
					oldStart, _ := strconv.Atoi(m[1])
					newStart, _ := strconv.Atoi(m[3])
					oldLineNum, newLineNum = oldStart, newStart
				}
				continue
			case strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++"):
				continue
			default:
				content := line
				if len(content) > 0 && content[0] == ' ' {
					content = content[1:]
				}
				code := truncateLineToDisplayWidth(content, diffWidth)
				displayLineNum := newLineNum
				if newLineNum < oldLineNum {
					displayLineNum = oldLineNum
				}
				rendered = DimStyle.Render(formatLineNum(displayLineNum)) + DimStyle.Render(" "+code)
				oldLineNum++
				newLineNum++
			}
			result = append(result, "  "+rendered)
			shownLines++
		}
	}
	if b.ToolName == tools.NameEdit && strings.TrimSpace(b.ResultContent) != "" && !b.toolResultIsError() && !b.toolResultIsCancelled() && !toolShouldHideSuccessfulFileOpResult(b) {
		result = append(result, ToolResultExpandedStyle.Render("  ↳ Diagnostics:"))
		result = append(result, renderLSPDiagnosticsLines(editSuccessDiagnosticsContent(b.ResultContent), "    ", cardWidth-4)...)
	}
	if b.toolResultIsError() && b.ResultContent != "" {
		if b.ToolName == tools.NameEdit {
			result = appendEditPatchPreview(result, b.editPatchArgsJSON(), cardWidth-4)
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

	return renderPrewrappedToolCard(blockStyle, cardWidth, toolCardTitle("TOOL CALL", b.ID), result, toolCardBg, railANSISeq("tool", b.Focused))
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

func editPatchPreviewLines(patch string) []string {
	lines := strings.Split(strings.TrimSpace(patch), "\n")
	if len(lines) > 20 {
		lines = append(lines[:20], "... (patch truncated)")
	}
	return lines
}

func (b *Block) diffToolFilePath() string {
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
		if rel, err := filepath.Rel(".", path); err == nil && rel != "" && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
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
