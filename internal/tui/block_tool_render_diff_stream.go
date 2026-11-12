package tui

import (
	"sort"
	"strings"

	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/syntax"
)

// streamingApplyPatchPreview is the memoized line model of the live patch
// preview. lineRowOffsets has one more entry than lineStarts and records how
// many screen rows precede each patch line; a boundary line occupies two rows
// (separator, then header), so the range renderer maps a screen row to its
// patch line instead of assuming a one-to-one mapping.
type streamingApplyPatchPreview struct {
	patch          string
	lineStarts     []int
	lineStates     []applyPatchPreviewState
	lineBoundaries []bool
	lineRowOffsets []int
}

// streamingApplyPatchCardLayout describes the small fixed part of a live
// apply_patch card and the preview rows that follow it. The layout deliberately
// keeps patch lines out of the fixed slice: a viewport can then render only the
// requested rows instead of assembling the complete card on every streamed
// update.
type streamingApplyPatchCardLayout struct {
	frame          prewrappedCardFrame
	fixedLines     []string
	tailLines      []string
	patch          string
	lineStarts     []int
	patchStates    []applyPatchPreviewState
	lineBoundaries []bool
	lineRowOffsets []int
	patchWidth     int
	syntaxPath     string
	patchStart     int
	patchRows      int
	totalLines     int
	highlighter    *syntax.Highlighter
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
	b.streamingPatchPreviewVersion = 0
	b.streamingPatchArgsLen = 0
	b.streamingPatchFromPreview = false
	b.streamingPatchMemoValid = false
	b.streamingPatchText = ""
	b.streamingPatchLineStarts = nil
	b.streamingPatchLineStates = nil
	b.streamingPatchLineBoundaries = nil
	b.streamingPatchLineRowOffsets = nil
}

func (b *Block) streamingApplyPatchPreviewData() streamingApplyPatchPreview {
	argsJSON := b.editPatchArgsJSON()
	fromPreview := strings.HasPrefix(strings.TrimSpace(b.patchPreviewText), "*** ")
	if b.streamingPatchMemoValid && b.streamingPatchPreviewVersion == b.patchPreviewVersion &&
		(b.streamingPatchFromPreview || b.streamingPatchArgsLen == len(argsJSON)) {
		return streamingApplyPatchPreview{
			patch:          b.streamingPatchText,
			lineStarts:     b.streamingPatchLineStarts,
			lineStates:     b.streamingPatchLineStates,
			lineBoundaries: b.streamingPatchLineBoundaries,
			lineRowOffsets: b.streamingPatchLineRowOffsets,
		}
	}

	patch := ""
	if fromPreview {
		// cachedApplyPatchStreamingArgs already runs the extractor, which
		// sanitizes the preview. Re-sanitizing the whole growing patch here
		// made every coalesced refresh linear in the full patch again.
		patch = b.patchPreviewText
	}
	if patch == "" && !b.toolArgumentsAreReceiving() {
		patch = sanitizeToolDisplayText(editPatchFromArgs(argsJSON))
	}
	if patch == "" {
		patch = sanitizeToolDisplayText(applyPatchStreamingPreview(argsJSON))
	}
	patch = strings.TrimSpace(patch)

	var lineStarts []int
	var lineStates []applyPatchPreviewState
	var lineBoundaries []bool
	var lineRowOffsets []int
	if patch != "" {
		appendOnly := b.streamingPatchMemoValid && len(b.streamingPatchLineStates) > 0 &&
			strings.HasPrefix(patch, b.streamingPatchText)
		if appendOnly {
			lineStarts = b.streamingPatchLineStarts
			lineStates = b.streamingPatchLineStates
			lineBoundaries = b.streamingPatchLineBoundaries
			lineRowOffsets = b.streamingPatchLineRowOffsets
		} else {
			lineStarts = []int{0}
			lineStates = []applyPatchPreviewState{{}}
			lineRowOffsets = []int{0}
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
		// Fold the per-line rule states. An append-only growth refolds from
		// the previous last line's own pre-state: that line grows with the
		// stream, and its classification can flip while it completes — a
		// marker's colon or a @@ header only arrives with later fragments.
		// Rows above it never change — a row's state is a function of the
		// rows above it — so their entries stand. Boundaries and row offsets
		// start from the same point: a line that turns into a section header
		// gains its separator row only on the refold.
		foldFrom := 0
		if appendOnly {
			foldFrom = len(lineStates) - 1
		}
		state := lineStates[foldFrom]
		lineStates = lineStates[:foldFrom+1]
		lineBoundaries = lineBoundaries[:min(foldFrom, len(lineBoundaries))]
		lineRowOffsets = lineRowOffsets[:min(foldFrom+1, len(lineRowOffsets))]
		for i := foldFrom; i < len(lineStarts); i++ {
			end := len(patch)
			if i+1 < len(lineStarts) {
				end = lineStarts[i+1] - 1
			}
			if i > foldFrom {
				lineStates = append(lineStates, state)
			}
			line := patch[lineStarts[i]:end]
			boundary := applyPatchPreviewBoundary(line, state)
			lineBoundaries = append(lineBoundaries, boundary)
			rows := 1
			if boundary {
				rows++
			}
			lineRowOffsets = append(lineRowOffsets, lineRowOffsets[i]+rows)
			state.advance(line)
		}
	}
	b.streamingPatchPreviewVersion = b.patchPreviewVersion
	b.streamingPatchArgsLen = len(argsJSON)
	b.streamingPatchFromPreview = fromPreview
	b.streamingPatchMemoValid = true
	b.streamingPatchText = patch
	b.streamingPatchLineStarts = lineStarts
	b.streamingPatchLineStates = lineStates
	b.streamingPatchLineBoundaries = lineBoundaries
	b.streamingPatchLineRowOffsets = lineRowOffsets
	return streamingApplyPatchPreview{
		patch:          patch,
		lineStarts:     lineStarts,
		lineStates:     lineStates,
		lineBoundaries: lineBoundaries,
		lineRowOffsets: lineRowOffsets,
	}
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
	// the preview's syntax highlighting (see diffToolHeaderSidesWithTargets).
	headerPath, headerTargetPath, syntaxPath := b.diffToolHeaderSidesWithTargets(targets)
	prefix := b.renderToolPrefix(spinnerFrame)
	headerLine := appendDiffHeaderSummary(
		renderToolHeaderLine(prefix, toolName),
		headerPath,
		headerTargetPath,
		mergeHeaderOptions("", b.diagnosticHeaderOptions()),
		b.fileDiffSummaryLine(targets, "", joinMoveSides(headerPath, headerTargetPath) != ""),
		cardWidth-4,
	)
	headerLine = buildToolHeaderLine(headerLine, b.ToolProgress, cardWidth, false, b.toolExecutionIsRunning())

	bodyLines := []string{
		headerLine,
	}
	bodyLines = b.appendApplyPatchTargetLines(bodyLines, targets, cardWidth-4)
	preview := b.streamingApplyPatchPreviewData()
	// Mirror the cold render (renderFileDiffCall): a patch whose targets only
	// delete or move/rename files has no line change to preview, so the
	// "Requested patch" section is suppressed there too. Args that are not yet
	// parseable leave targets empty and keep the transient preview.
	if applyPatchOnlyMoveOrDeleteTargets(targets) {
		preview = streamingApplyPatchPreview{}
	}
	if preview.patch != "" {
		bodyLines = append(bodyLines, toolFieldSection(currentStyles.ToolResultExpandedStyle, "Requested patch"))
	}
	diagnosticBody := b.appendToolArgDiagnostics(append([]string(nil), bodyLines...), max(cardWidth-4, 10))
	tailLines := diagnosticBody[len(bodyLines):]
	bodyLines[0] = diagnosticBody[0]

	fixedLines := []string{
		toolCardTitle("TOOL CALL", b.displayLabelID()),
		"",
	}
	fixedLines = append(fixedLines, bodyLines...)
	frame := newPrewrappedCardFrame(blockStyle, cardWidth, toolCardBg, cardRailPrefix("tool", b.Focused))
	contentStart := frame.marginTop + frame.padTop
	patchStart := contentStart + len(fixedLines)
	patchRows := 0
	if n := len(preview.lineRowOffsets); n > 0 {
		patchRows = preview.lineRowOffsets[n-1]
	}
	tailStart := patchStart + patchRows
	total := tailStart + len(tailLines) + frame.padBottom + frame.marginBottom
	return streamingApplyPatchCardLayout{
		frame:          frame,
		fixedLines:     fixedLines,
		tailLines:      tailLines,
		patch:          preview.patch,
		lineStarts:     preview.lineStarts,
		patchStates:    preview.lineStates,
		lineBoundaries: preview.lineBoundaries,
		lineRowOffsets: preview.lineRowOffsets,
		patchWidth:     cardWidth - 4,
		syntaxPath:     syntaxPath,
		patchStart:     patchStart,
		patchRows:      patchRows,
		totalLines:     total,
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
	patchEnd := layout.patchStart + layout.patchRows
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
				rowIndex := lineIndex - layout.patchStart
				// A boundary line owns two rows: the separator and the header.
				// Find the patch line whose row range contains rowIndex.
				patchLineIndex := sort.Search(len(layout.lineRowOffsets)-1, func(i int) bool {
					return layout.lineRowOffsets[i+1] > rowIndex
				})
				rowInLine := rowIndex - layout.lineRowOffsets[patchLineIndex]
				if rowInLine == 0 && layout.lineBoundaries[patchLineIndex] {
					out = append(out, layout.renderBodyLine(applyPatchDiffSeparator(layout.patchWidth)))
					continue
				}
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
	return layout.frame.renderBodyLine(raw)
}
