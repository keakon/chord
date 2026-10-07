package tui

// SetCursorPosition places the caret in a logical line without replaying visual
// up/down movement, which cannot always traverse synthetic wrapped cursor rows.
func (i *Input) SetCursorPosition(row, col int) {
	row = min(max(row, 0), i.LineCount()-1)
	col = max(col, 0)
	beforeRow, beforeCol := i.Line(), i.Column()
	start := runeOffsetFromRowCol(i.DisplayValue(), row, 0)
	for visualRow, displayRow := range i.selectionDisplayRows() {
		if displayRow.start == start {
			// The component's coordinate API positions directly in its buffer.
			// Input owns selection, so discard this temporary native selection.
			i.textarea.BeginSelection(inputPromptWidth, visualRow-i.ScrollYOffset())
			i.textarea.ClearSelection()
			break
		}
	}
	i.textarea.SetCursorColumn(col)
	if i.Line() != beforeRow || i.Column() != beforeCol {
		i.interactionVersion++
	}
	// Reposition around the caret without resetting the visible window.
	i.textarea.SetHeight(i.Height())
}

func (i *Input) moveCursorVertical(down bool) {
	row, col := i.Line(), i.Column()
	if down {
		i.textarea.CursorDown()
	} else {
		i.textarea.CursorUp()
	}
	if i.Line() != row || i.Column() != col {
		i.interactionVersion++
	}
}
