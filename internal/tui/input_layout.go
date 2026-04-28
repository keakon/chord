package tui

import "strings"

const (
	inputMinLines = 1
	inputMaxLines = 8
)

// clampedDisplayLineCount returns the number of visible rows after soft wrap (same rules
// as selection / bubbles textarea), clamped to [inputMinLines, inputMaxLines].
// Result is cached by (value, width) so repeated calls during View()/recalcViewportSize()
// do not re-run the expensive wrappedContentLines() on every streaming token.
func (i *Input) clampedDisplayLineCount() int {
	val := i.textarea.Value()
	width := i.inputContentWidth()
	if i.displayLineCacheResult != 0 && val == i.displayLineCacheVal && width == i.displayLineCacheWidth {
		return i.displayLineCacheResult
	}
	lines := i.totalDisplayLineCount()
	if lines < inputMinLines {
		lines = inputMinLines
	}
	if lines > inputMaxLines {
		lines = inputMaxLines
	}
	i.displayLineCacheResult = lines
	i.displayLineCacheVal = val
	i.displayLineCacheWidth = width
	return lines
}

// ClampedDisplayLineCount is the display-line count used for composer layout
// (inputAreaHeight, syncHeight). See clampedDisplayLineCount.
func (i *Input) ClampedDisplayLineCount() int {
	return i.clampedDisplayLineCount()
}

// syncHeight adjusts the textarea's visible height to match its content
// and fixes the viewport scroll offset.
//
// Background: textarea.SetHeight calls repositionView which ensures the cursor
// is within the visible bounds, but does NOT scroll up to minimize the offset.
// After inserting newlines (typing or paste) with the old (smaller) height,
// the viewport scrolls down to keep the cursor visible; increasing the height
// afterward leaves the offset unchanged, hiding top lines and showing empty
// trailing lines.
//
// Fix: when the height has increased (content was added while height was
// smaller) or all content fits, reset the scroll offset by moving the cursor
// to the top and restoring it so repositionView recalculates the minimal offset.
// We avoid doing this when height is unchanged and content exceeds max height,
// as that would disrupt normal scrolling during line-by-line navigation.
func (i *Input) syncHeight() {
	lines := i.clampedDisplayLineCount()
	oldHeight := i.textarea.Height()
	i.textarea.SetHeight(lines)

	// Only fix scroll when:
	// - height increased (content was added with a smaller viewport), OR
	// - all content fits within the viewport (offset should be 0)
	heightGrew := lines > oldHeight
	allFits := i.totalDisplayLineCount() <= lines
	if (heightGrew || allFits) && i.textarea.ScrollYOffset() > 0 {
		// Use visual cursor row (not logical Line()) so that CursorDown()
		// — which moves by visual rows — restores the cursor correctly for
		// soft-wrapped content. Using Line() (logical) here causes the cursor
		// to land on the wrong visual row when the preceding line wraps.
		visualRow := i.visualCursorRow()
		col := i.textarea.Column()
		i.textarea.MoveToBegin()
		for range visualRow {
			i.textarea.CursorDown()
		}
		i.textarea.SetCursorColumn(col)
	}
}

// visualCursorRow returns the visual (display) row index of the cursor,
// accounting for soft-wrapped lines. This matches the movement unit of
// CursorDown(), which moves by visual rows rather than logical lines.
func (i *Input) visualCursorRow() int {
	logicalRow := i.textarea.Line()
	li := i.textarea.LineInfo()
	width := i.inputContentWidth()
	val := i.textarea.Value()
	rawLines := strings.Split(val, "\n")
	visual := 0
	for r := 0; r < logicalRow && r < len(rawLines); r++ {
		wrapped := inputWrap([]rune(rawLines[r]), width)
		if len(wrapped) == 0 {
			visual++
		} else {
			visual += len(wrapped)
		}
	}
	visual += li.RowOffset
	return visual
}

// preExpandHeight pre-expands the textarea height by 1 line before a newline
// is inserted. This prevents the textarea from scrolling when the new line is
// added, avoiding the first-line-hidden / extra-trailing-line bug.
// Uses clampedDisplayLineCount (visual lines after soft wrap) instead of
// LineCount (logical lines) so that wrapped lines are counted correctly.
func (i *Input) preExpandHeight() {
	lines := min(i.clampedDisplayLineCount()+1, inputMaxLines)
	i.textarea.SetHeight(lines)
}
