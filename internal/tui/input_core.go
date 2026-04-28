package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Input is a lightweight wrapper around textarea.Model that adds
// command-history navigation and multiline support.
type inputHistoryEntry struct {
	Display      string
	BangMode     bool
	InlinePastes []inlineLargePaste
	NextPasteSeq int
}

type inputDraftSnapshot struct {
	Entry inputHistoryEntry
	Row   int
	Col   int
}

type Input struct {
	textarea textarea.Model
	history  []inputHistoryEntry
	histIdx  int // index into history; len(history) means "current (new) entry"
	draft    inputDraftSnapshot
	// shellLine: heap bool so PromptFunc (closure) stays correct when Input is copied into Model.
	shellLine    *bool
	selection    inputSelection
	inlinePastes []inlineLargePaste
	nextPasteSeq int

	// displayLineCache caches the result of clampedDisplayLineCount to avoid
	// re-running the expensive wrappedContentLines() on every View()/recalcViewportSize().
	// Invalidated when content or width changes.
	displayLineCacheResult int
	displayLineCacheVal    string
	displayLineCacheWidth  int
}

// NewInput creates a focused Input ready for use.
func NewInput() Input {
	shell := new(bool)
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0 // unlimited
	ta.MaxHeight = 0 // unlimited lines (default is 99)
	ta.SetHeight(1)  // initial 1 content line

	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			if *shell {
				return "! "
			}
			return "> "
		}
		return "  "
	})

	// Remap keys: enter inserts newline by default in textarea;
	// we want shift+enter / ctrl+j for newline, and enter for submit (handled outside).
	// Disable textarea's built-in InsertNewline so the parent can intercept enter.
	km := ta.KeyMap
	km.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	// Disable up/down line navigation so parent can use them for history.
	km.LineNext.SetKeys()
	km.LinePrevious.SetKeys()
	ta.KeyMap = km

	ta.SetStyles(newTextareaStyles())
	ta.SetVirtualCursor(false)
	ta.Focus()
	return Input{textarea: ta, shellLine: shell}
}

// SyncNewlineKeys updates the textarea's internal InsertNewline binding to match
// the TUI KeyMap. Must be called whenever the KeyMap changes so that the
// textarea recognizes remapped newline keys.
func (i *Input) SyncNewlineKeys(keys []string) {
	if len(keys) > 0 {
		i.textarea.KeyMap.InsertNewline.SetKeys(keys...)
	}
}

// SetBangMode switches the first-line prompt between "> " and "! " (shell line).
func (i *Input) SetBangMode(on bool) {
	if i.shellLine == nil {
		i.shellLine = new(bool)
	}
	*i.shellLine = on
	i.ClearSelection()
}

// BangMode reports whether the input is in local shell line mode.
func (i *Input) BangMode() bool {
	return i.shellLine != nil && *i.shellLine
}

func newTextareaStyles() textarea.Styles {
	s := textarea.DefaultDarkStyles()
	s.Focused.Prompt = InputPromptStyle
	s.Blurred.Prompt = InputPromptStyle
	// Remove the default cursor-line background so the input area blends with the app background.
	s.Focused.CursorLine = lipgloss.NewStyle()
	return s
}

// Focus gives the underlying textarea focus and returns the cursor-blink command.
func (i *Input) Focus() tea.Cmd {
	return i.textarea.Focus()
}

// Blur removes focus from the underlying textarea.
func (i *Input) Blur() {
	i.textarea.Blur()
}

// Value returns the current input text.
func (i *Input) Value() string {
	return i.textarea.Value()
}

func (i *Input) historyEntry() inputHistoryEntry {
	return inputHistoryEntry{
		Display:      i.DisplayValue(),
		BangMode:     i.BangMode(),
		InlinePastes: copyInlineLargePastes(i.inlinePastes),
		NextPasteSeq: i.nextPasteSeq,
	}
}

func (i *Input) draftSnapshot() inputDraftSnapshot {
	return inputDraftSnapshot{
		Entry: i.historyEntry(),
		Row:   i.Line(),
		Col:   i.Column(),
	}
}

func (i *Input) applyHistoryEntry(entry inputHistoryEntry) {
	if i.shellLine == nil {
		i.shellLine = new(bool)
	}
	*i.shellLine = entry.BangMode
	i.textarea.SetValue(entry.Display)
	i.inlinePastes = copyInlineLargePastes(entry.InlinePastes)
	if entry.NextPasteSeq < 0 {
		entry.NextPasteSeq = 0
	}
	i.nextPasteSeq = entry.NextPasteSeq
	i.ensureCursorOutsideInlinePastes()
	i.ClearSelection()
}

func (i *Input) applyDraftSnapshot(snapshot inputDraftSnapshot) {
	i.applyHistoryEntry(snapshot.Entry)
	i.textarea.MoveToBegin()
	for j := 0; j < snapshot.Row; j++ {
		i.textarea.CursorDown()
	}
	i.textarea.SetCursorColumn(snapshot.Col)
	i.ensureCursorOutsideInlinePastes()
}

func (i *Input) SetValue(s string) {
	if i.shellLine != nil {
		*i.shellLine = false
	}
	i.textarea.SetValue(s)
	i.inlinePastes = nil
	i.ClearSelection()
}

// InsertString inserts a string at the current cursor position.
func (i *Input) InsertString(s string) {
	i.textarea.InsertString(s)
	i.inlinePastes = nil
	i.ClearSelection()
}

// SetWidth adjusts the textarea width (call on resize).
func (i *Input) SetWidth(w int) {
	i.textarea.SetWidth(w)
	i.ClearSelection()
}

// SetHeight adjusts the visible height of the textarea (content lines, excluding border).
func (i *Input) SetHeight(h int) {
	i.textarea.SetHeight(h)
}

// Height returns the visible height of the textarea content area.
func (i *Input) Height() int {
	return i.textarea.Height()
}

// LineCount returns the number of content lines currently in the textarea.
func (i *Input) LineCount() int {
	return i.textarea.LineCount()
}

// Line returns the 0-indexed row position of the cursor.
func (i *Input) Line() int {
	return i.textarea.Line()
}

// Column returns the 0-indexed column of the cursor on the current line.
func (i *Input) Column() int {
	return i.textarea.Column()
}

// View renders the input widget.
func (i *Input) View() string {
	return i.textarea.View()
}

// remapInlinePastesAfterEdit remaps placeholder ranges across a single textarea
// text edit, as long as the edited rune range does not intersect any placeholder.
//
// It uses the longest common prefix/suffix between before and after to identify a
// single changed window. Placeholders entirely before that window stay unchanged;
// placeholders entirely after it shift by the rune delta.
func remapInlinePastesAfterEdit(before, after string, pastes []inlineLargePaste) ([]inlineLargePaste, bool) {
	if len(pastes) == 0 {
		return nil, true
	}
	beforeRunes := []rune(before)
	afterRunes := []rune(after)
	prefix := 0
	for prefix < len(beforeRunes) && prefix < len(afterRunes) && beforeRunes[prefix] == afterRunes[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(beforeRunes)-prefix && suffix < len(afterRunes)-prefix && beforeRunes[len(beforeRunes)-1-suffix] == afterRunes[len(afterRunes)-1-suffix] {
		suffix++
	}
	beforeTailStart := len(beforeRunes) - suffix
	delta := len(afterRunes) - len(beforeRunes)
	remapped := copyInlineLargePastes(pastes)
	for idx := range remapped {
		p := remapped[idx]
		if p.Start < 0 || p.End < p.Start || p.End > len(beforeRunes) {
			return nil, false
		}
		switch {
		case p.End <= prefix:
			// Placeholder is fully before the changed window.
		case p.Start >= beforeTailStart:
			// Placeholder is fully after the changed window.
			p.Start += delta
			p.End += delta
			remapped[idx] = p
		default:
			// Edit intersects placeholder; caller must drop preservation.
			return nil, false
		}
		if p.Start < 0 || p.End < p.Start || p.End > len(afterRunes) {
			return nil, false
		}
		if string(afterRunes[p.Start:p.End]) != p.DisplayText {
			return nil, false
		}
	}
	return remapped, true
}

// Update forwards a tea.Msg to the underlying textarea.
// Note: height syncing is handled by the parent (app.go) before rendering.
func (i *Input) Update(msg tea.Msg) tea.Cmd {
	before := i.DisplayValue()
	beforePastes := copyInlineLargePastes(i.inlinePastes)
	var cmd tea.Cmd
	i.textarea, cmd = i.textarea.Update(msg)
	after := i.DisplayValue()
	if len(beforePastes) > 0 {
		switch {
		case before == after:
			if !i.inlinePastesValid() {
				i.inlinePastes = nil
			}
		case i.inlinePastesValid():
			// Common case: edit happened fully after the last placeholder or before
			// the first one without invalidating any stored ranges.
		case func() bool {
			remapped, ok := remapInlinePastesAfterEdit(before, after, beforePastes)
			if ok {
				i.inlinePastes = remapped
				return true
			}
			return false
		}():
			// remapped successfully
		default:
			i.inlinePastes = nil
		}
	}
	i.ensureCursorOutsideInlinePastes()
	return cmd
}

// Focused returns true if the textarea currently has focus.
func (i *Input) Focused() bool {
	return i.textarea.Focused()
}

func (i *Input) ScrollYOffset() int {
	return i.textarea.ScrollYOffset()
}

// Cursor returns the terminal cursor position for the textarea.
func (i *Input) Cursor() *tea.Cursor {
	return i.textarea.Cursor()
}

// CursorEnd moves the cursor to the end of the input.
func (i *Input) CursorEnd() {
	i.textarea.CursorEnd()
}

// SetCursorPosition moves the cursor to the given 0-based row and column.
func (i *Input) SetCursorPosition(row, col int) {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	i.textarea.MoveToBegin()
	for j := 0; j < row; j++ {
		i.textarea.CursorDown()
	}
	i.textarea.SetCursorColumn(col)
}

func runeOffsetFromRowCol(s string, row, col int) int {
	if row < 0 {
		row = 0
	}
	if col < 0 {
		col = 0
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return 0
	}
	if row >= len(lines) {
		row = len(lines) - 1
	}
	offset := 0
	for i := 0; i < row; i++ {
		offset += len([]rune(lines[i])) + 1
	}
	lineRunes := []rune(lines[row])
	if col > len(lineRunes) {
		col = len(lineRunes)
	}
	return offset + col
}

func rowColFromRuneOffset(s string, offset int) (row, col int) {
	if offset < 0 {
		offset = 0
	}
	remaining := offset
	for idx, line := range strings.Split(s, "\n") {
		lineLen := len([]rune(line))
		if remaining <= lineLen {
			return idx, remaining
		}
		remaining -= lineLen
		if remaining == 0 {
			return idx, lineLen
		}
		remaining--
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return 0, 0
	}
	last := len(lines) - 1
	return last, len([]rune(lines[last]))
}

// ReplaceRuneRangePreserveInlinePastes replaces the [start,end) rune range in the
// current display value with replacement, while preserving any inline large-paste
// placeholders that are fully outside the replaced range.
//
// This is used by features like @-mention completion which need to modify the
// composer text without destroying inline paste metadata (which would otherwise
// cause the submitted message to contain only placeholder text).
//
// Returns false if the requested range intersects an inline paste placeholder,
// since editing inside placeholders is not supported.
func (i *Input) ReplaceRuneRangePreserveInlinePastes(start, end int, replacement string) bool {
	display := i.DisplayValue()
	runes := []rune(display)
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > len(runes) {
		start = len(runes)
	}
	if end > len(runes) {
		end = len(runes)
	}

	// Reject edits that intersect any placeholder.
	for _, paste := range i.inlinePastes {
		// Intersection test for half-open intervals.
		if start < paste.End && end > paste.Start {
			return false
		}
	}

	replRunes := []rune(replacement)
	delta := len(replRunes) - (end - start)
	newRunes := make([]rune, 0, len(runes)+max(0, delta))
	newRunes = append(newRunes, runes[:start]...)
	newRunes = append(newRunes, replRunes...)
	newRunes = append(newRunes, runes[end:]...)

	// Shift placeholders that are after the replaced region.
	for idx := range i.inlinePastes {
		p := i.inlinePastes[idx]
		if p.Start >= end {
			p.Start += delta
			p.End += delta
			i.inlinePastes[idx] = p
		}
	}

	i.rebuildDisplay(string(newRunes), start+len(replRunes))
	i.ClearSelection()
	return true
}
