package composer

// Revision identifies the current draft and invalidates asynchronous edit results.
type Revision struct{ Boundary, Interaction uint64 }

func (i *Input) Revision() Revision { return Revision{i.editBoundary, i.interactionVersion} }

// BeginDraft invalidates work owned by the previous draft without changing text.
func (i *Input) BeginDraft() { i.editBoundary++ }

// ContentChanged invalidates pending operations after an external attachment edit.
func (i *Input) ContentChanged() { i.interactionVersion++ }

// Suspend transfers keyboard focus without discarding the current selection.
func (i *Input) Suspend() { i.textarea.Blur() }

// State saves an editor's draft and navigation position. History itself is shared
// across agent drafts. State is opaque so restoring it preserves editor invariants.
type State struct {
	draft        Draft
	browsing     bool
	index        int
	historyDraft Draft
}

func (i *Input) SaveState() State {
	return State{i.Snapshot(), i.histIdx < len(i.history), i.histIdx, i.draft}
}

func (i *Input) RestoreState(s State) {
	i.Restore(s.draft)
	if s.browsing {
		i.histIdx = min(max(s.index, 0), len(i.history))
		i.draft = s.historyDraft
	} else {
		i.EndHistoryNavigation()
	}
	i.SyncHeight()
}

// EndHistoryNavigation keeps the displayed draft and leaves history browsing.
func (i *Input) EndHistoryNavigation() { i.histIdx = len(i.history); i.draft = Draft{} }

// WordRangeAt resolves a word in the visible row using source rune offsets.
func (i *Input) WordRangeAt(x, y int) (int, int) {
	offset, ok := i.SelectionPositionAt(x, y)
	if !ok {
		return 0, 0
	}
	return i.selectionWordRange(offset, i.selectionDisplayRows()[y+i.ScrollYOffset()])
}

// LineRangeAt returns the source rune interval of a visible wrapped row.
func (i *Input) LineRangeAt(y int) (int, int) {
	rows := i.selectionDisplayRows()
	n := y + i.ScrollYOffset()
	if y < 0 || y >= i.Height() || n >= len(rows) {
		return 0, 0
	}
	return rows[n].start, rows[n].end
}

// HasContent reports whether the saved draft contains text or inline objects.
func (s State) HasContent() bool {
	return s.draft.Entry.Display != "" || len(s.draft.Entry.InlinePastes) > 0
}

// TokenAt returns the inline object covering a display rune offset. The returned
// value shares no mutable state with the editor.
func (i *Input) TokenAt(offset int) (Token, bool) {
	for _, token := range i.inlinePastes {
		if offset >= token.Start && offset < token.End {
			return token, true
		}
	}
	return Token{}, false
}
