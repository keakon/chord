package tui

import (
	"strings"

	"github.com/keakon/chord/internal/tools"
)

// streamingApplyPatchCardLayout describes the small fixed part of a live
// apply_patch card and the one-line-per-input-line preview that follows it.
// The layout deliberately keeps patch lines out of the fixed slice: a
// viewport can then render only the requested rows instead of assembling the
// complete card on every streamed update.
type streamingApplyPatchCardLayout struct {
	frame       prewrappedCardFrame
	fixedLines  []string
	tailLines   []string
	patch       string
	lineStarts  []int
	patchWidth  int
	filePath    string
	syntaxPath  string
	patchStart  int
	totalLines  int
	highlighter *codeHighlighter
}

func (b *Block) streamingApplyPatchRangeEligible() bool {
	return b != nil &&
		b.Type == BlockToolCall &&
		toolNameKey(b.ToolName) == tools.NameApplyPatch &&
		b.toolArgumentsAreReceiving() &&
		!b.ResultDone &&
		!b.Collapsed &&
		strings.TrimSpace(b.ResultContent) == "" &&
		strings.TrimSpace(b.Diff) == "" &&
		!b.toolResultIsError() &&
		!b.toolResultIsCancelled()
}

func (b *Block) clearStreamingApplyPatchRangeMemo() {
	b.streamingPatchArgsLen = 0
	b.streamingPatchMemoValid = false
	b.streamingPatchText = ""
	b.streamingPatchLineStarts = nil
}

func (b *Block) streamingApplyPatchPreviewData() (string, []int) {
	argsJSON := b.editPatchArgsJSON()
	if b.streamingPatchMemoValid && b.streamingPatchArgsLen == len(argsJSON) {
		return b.streamingPatchText, b.streamingPatchLineStarts
	}

	patch := ""
	if strings.HasPrefix(strings.TrimSpace(b.patchPreviewText), "*** ") {
		patch = sanitizeToolDisplayText(b.patchPreviewText)
	}
	if patch == "" {
		patch = sanitizeToolDisplayText(editPatchFromArgs(argsJSON))
	}
	if patch == "" {
		patch = sanitizeToolDisplayText(applyPatchStreamingPreview(argsJSON))
	}
	patch = strings.TrimSpace(patch)

	var lineStarts []int
	if patch != "" {
		appendOnly := b.streamingPatchMemoValid && strings.HasPrefix(patch, b.streamingPatchText)
		if appendOnly {
			lineStarts = b.streamingPatchLineStarts
		} else {
			lineStarts = []int{0}
		}
		scanFrom := 0
		if appendOnly {
			scanFrom = len(b.streamingPatchText)
		}
		for i := scanFrom; i < len(patch); i++ {
			if patch[i] == '\n' {
				lineStarts = append(lineStarts, i+1)
			}
		}
	}
	b.streamingPatchArgsLen = len(argsJSON)
	b.streamingPatchMemoValid = true
	b.streamingPatchText = patch
	b.streamingPatchLineStarts = lineStarts
	return patch, lineStarts
}

func (b *Block) streamingApplyPatchLineCount(width int) int {
	layout := b.streamingApplyPatchCardLayout(width, "")
	return layout.totalLines
}

func (b *Block) streamingApplyPatchCardLayout(width int, spinnerFrame string) streamingApplyPatchCardLayout {
	metrics := newToolCardMetrics(width)
	blockStyle := metrics.blockStyle
	toolCardBg := metrics.toolCardBg
	cardWidth := metrics.cardWidth
	toolName := toolNameKey(b.ToolName)

	targets := b.applyPatchTargets()
	// The header path may be a display summary; keep the undecorated target for
	// the preview's syntax highlighting (see diffToolPathsWithTargets).
	filePath, syntaxPath := b.diffToolPathsWithTargets(targets)
	if filePath != "" {
		filePath = b.displayToolPath(filePath)
	}
	prefix := b.renderToolPrefix(spinnerFrame)
	headerLine := appendSearchHeaderSummary(
		renderToolHeaderLine(prefix, toolName),
		filePath,
		mergeHeaderOptions("", b.diagnosticHeaderOptions()),
		b.fileDiffSummaryLine(targets, ""),
		cardWidth-4,
	)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())

	bodyLines := []string{
		headerLine,
	}
	bodyLines = appendApplyPatchTargetLines(bodyLines, targets, cardWidth-4)
	patch, lineStarts := b.streamingApplyPatchPreviewData()
	// Mirror the cold render (renderFileDiffCall): a patch whose targets only
	// delete or move/rename files has no line change to preview, so the
	// "Requested patch" section is suppressed there too. Args that are not yet
	// parseable leave targets empty and keep the transient preview.
	if applyPatchOnlyMoveOrDeleteTargets(targets) {
		patch, lineStarts = "", nil
	}
	if patch != "" {
		bodyLines = append(bodyLines, toolFieldSection(ToolResultExpandedStyle, "Requested patch"))
	}
	diagnosticBody := b.appendToolArgDiagnostics(append([]string(nil), bodyLines...), max(cardWidth-4, 10))
	tailLines := diagnosticBody[len(bodyLines):]
	bodyLines[0] = diagnosticBody[0]

	fixedLines := []string{
		toolCardTitle("TOOL CALL", b.displayLabelID()),
		"",
	}
	fixedLines = append(fixedLines, bodyLines...)
	frame := newPrewrappedCardFrame(blockStyle, cardWidth, toolCardBg, railANSISeq("tool", b.Focused))
	contentStart := frame.marginTop + frame.padTop
	patchStart := contentStart + len(fixedLines)
	tailStart := patchStart + len(lineStarts)
	total := tailStart + len(tailLines) + frame.padBottom + frame.marginBottom
	return streamingApplyPatchCardLayout{
		frame:      frame,
		fixedLines: fixedLines,
		tailLines:  tailLines,
		patch:      patch,
		lineStarts: lineStarts,
		patchWidth: cardWidth - 4,
		filePath:   filePath,
		syntaxPath: syntaxPath,
		patchStart: patchStart,
		totalLines: total,
	}
}

func (b *Block) renderStreamingApplyPatchRange(width int, spinnerFrame string, start, end int) []string {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	layout := b.streamingApplyPatchCardLayout(width, spinnerFrame)
	if start >= layout.totalLines {
		return nil
	}
	if end > layout.totalLines {
		end = layout.totalLines
	}
	if end <= start {
		return nil
	}

	if layout.patch != "" {
		layout.highlighter = b.applyPatchPreviewHighlighter(layout.syntaxPath, layout.patch)
	}
	out := make([]string, 0, end-start)
	contentStart := layout.frame.marginTop + layout.frame.padTop
	patchEnd := layout.patchStart + len(layout.lineStarts)
	tailStart := patchEnd
	tailEnd := tailStart + len(layout.tailLines)
	padBottomEnd := tailEnd + layout.frame.padBottom
	for lineIndex := start; lineIndex < end; lineIndex++ {
		switch {
		case lineIndex < layout.frame.marginTop:
			out = append(out, layout.frame.blankMargin)
		case lineIndex < contentStart:
			out = append(out, layout.frame.blankWrapped)
		case lineIndex >= padBottomEnd:
			out = append(out, layout.frame.blankMargin)
		case lineIndex >= tailEnd:
			out = append(out, layout.frame.blankWrapped)
		case lineIndex >= layout.patchStart:
			if lineIndex < patchEnd {
				patchLineIndex := lineIndex - layout.patchStart
				lineStart := layout.lineStarts[patchLineIndex]
				lineEnd := len(layout.patch)
				if patchLineIndex+1 < len(layout.lineStarts) {
					lineEnd = layout.lineStarts[patchLineIndex+1] - 1
				}
				raw := renderApplyPatchPreviewLine(layout.patch[lineStart:lineEnd], layout.patchWidth, layout.highlighter)
				out = append(out, layout.renderBodyLine(raw))
				continue
			}
			raw := layout.tailLines[lineIndex-tailStart]
			out = append(out, layout.renderBodyLine(raw))
		default:
			contentIndex := lineIndex - contentStart
			out = append(out, layout.renderBodyLine(layout.fixedLines[contentIndex]))
		}
	}
	return out
}

func (layout streamingApplyPatchCardLayout) renderBodyLine(raw string) string {
	// renderPrewrappedToolCard preserves the card background before handing
	// lines to the frame. Mirror that ordering so a range slice is byte-for-byte
	// identical to the existing cold full render.
	return layout.frame.renderBodyLine(preserveBackground(raw, layout.frame.bgColorNum))
}
