package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

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

func writeDiffToContentLines(diff string) []string {
	var out []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			out = append(out, line[1:])
		}
	}
	return out
}

// appendEditToolUnifiedDiffPair renders one logical (-,+) line pair from a unified diff.
func appendEditToolUnifiedDiffPair(result *[]string, oldLine, newLine string, oldLineNum, newLineNum, diffWidth int, hl *codeHighlighter) int {
	formatLineNum := func(n int) string { return fmt.Sprintf("%4d ", n) }
	if lines := renderInlineDiffLine(oldLine, newLine, diffWidth); lines != nil {
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
	blockStyle := ToolBlockStyle
	toolCardBg := currentTheme.ToolCallBg
	boxWidth := width - blockStyle.GetHorizontalMargins()
	if boxWidth < 10 {
		boxWidth = 10
	}
	cardWidth := boxWidth - blockStyle.GetHorizontalPadding() - blockStyle.GetHorizontalBorderSize()
	if cardWidth < 10 {
		cardWidth = 10
	}
	var filePath string
	var parsed struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(b.Content), &parsed) == nil {
		filePath = parsed.Path
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	var result []string
	headerLine := fmt.Sprintf("  %s %s", prefix, ToolCallStyle.Render(b.ToolName))
	if filePath != "" {
		headerLine += " " + DimStyle.Render(filePath)
	}
	headerLine = appendToolProgressSuffix(headerLine, b.ToolProgress, cardWidth-4)
	result = append(result, headerLine)
	if b.Collapsed {
		return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
	}
	const diffLineNumWidth = 5
	diffWidth := cardWidth - 4 - diffLineNumWidth
	if diffWidth < 10 {
		diffWidth = 10
	}
	hl := ensureCodeHighlighter(&b.diffHL, filePath, diffContentSample(b.Diff))
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
								shownLines += appendEditToolUnifiedDiffPair(&result, delBodies[k], addBodies[k], oldLineNum, newLineNum, diffWidth, hl)
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
						shownLines += appendEditToolUnifiedDiffPair(&result, line[1:], next[1:], oldLineNum, newLineNum, diffWidth, hl)
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
				rendered = DimStyle.Render(formatLineNum(oldLineNum)) + DimStyle.Render(" "+code)
				oldLineNum++
				newLineNum++
			}
			result = append(result, "  "+rendered)
			shownLines++
		}
	}
	if writeEditToolResultExtraVisible(b) {
		result = append(result, ToolResultExpandedStyle.Render("  ↳ Result:"))
		result = append(result, renderLSPDiagnosticsLines(b.ResultContent, "    ", cardWidth-4)...)
	}
	if b.toolResultIsError() && b.ResultContent != "" {
		result = append(result, ErrorStyle.Render("  ↳ Error:"))
		result = append(result, renderLSPDiagnosticsLines(b.ResultContent, "    ", cardWidth-4)...)
	} else if b.toolResultIsCancelled() && b.ResultContent != "" {
		result = append(result, DimStyle.Render("  ↳ Cancelled"))
		if detail := toolCancelledDetailText(b.ResultContent); detail != "" {
			result = append(result, renderLSPDiagnosticsLines(detail, "    ", cardWidth-4)...)
		}
	}
	result = appendToolElapsedFooter(result, b)

	return renderPrewrappedToolCard(blockStyle, cardWidth, ToolLabelStyle.Render("TOOL CALL"), result, toolCardBg, railANSISeq("tool", b.Focused))
}
