package tui

// Reset clears the input and resets history navigation.
func (i *Input) Reset() {
	i.textarea.Reset()
	if i.shellLine != nil {
		*i.shellLine = false
	}
	i.histIdx = len(i.history)
	i.draft = inputDraftSnapshot{}
	i.inlinePastes = nil
	i.nextPasteSeq = 0
	i.ClearSelection()
}

// AddHistory appends a non-empty entry to history and resets navigation.
func (i *Input) AddHistory(s string) {
	i.PushHistory(inputHistoryEntry{Display: s})
}

// AddHistoryEntry appends a structured entry to history and resets navigation.
func (i *Input) AddHistoryEntry(entry inputHistoryEntry) {
	i.PushHistory(entry)
}

// AddCurrentToHistory appends the current input state to history.
func (i *Input) AddCurrentToHistory() {
	i.PushHistory(i.historyEntry())
}

// PushHistory appends a non-empty entry to history.
func (i *Input) PushHistory(entry inputHistoryEntry) {
	if entry.Display != "" {
		i.history = append(i.history, entry)
	}
	i.histIdx = len(i.history)
	i.draft = inputDraftSnapshot{}
	if i.textarea.Value() == "" {
		i.inlinePastes = nil
		i.nextPasteSeq = 0
	}
}

// HistoryUp navigates to the previous history entry.
// If the cursor is not on the first line, it moves the cursor up instead and returns false.
func (i *Input) HistoryUp() bool {
	if i.textarea.Line() > 0 {
		i.textarea.CursorUp()
		return false
	}
	if len(i.history) == 0 {
		return true
	}
	if i.histIdx == len(i.history) {
		i.draft = i.draftSnapshot()
	}
	if i.histIdx > 0 {
		i.histIdx--
		i.applyHistoryEntry(i.history[i.histIdx])
		i.textarea.CursorEnd()
		i.syncHeight()
	}
	return true
}

// HistoryDown navigates to the next history entry (or draft).
// If the cursor is not on the last line, it moves the cursor down instead and returns false.
func (i *Input) HistoryDown() bool {
	if i.textarea.Line() < i.textarea.LineCount()-1 {
		i.textarea.CursorDown()
		return false
	}
	if i.histIdx >= len(i.history) {
		return true
	}
	i.histIdx++
	if i.histIdx == len(i.history) {
		i.applyDraftSnapshot(i.draft)
	} else {
		i.applyHistoryEntry(i.history[i.histIdx])
		i.textarea.CursorEnd()
	}
	i.syncHeight()
	return true
}
