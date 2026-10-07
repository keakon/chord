package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/bubbles/v2/textarea"
)

// Canonicalize inserted text with the component's own sanitizer before computing
// rune deltas. Otherwise tabs/control characters can shift surviving objects.
func canonicalInputText(text string) string {
	if strings.IndexFunc(text, func(r rune) bool { return r == utf8.RuneError || r != '\n' && unicode.IsControl(r) }) < 0 {
		return text
	}
	ta := textarea.New()
	ta.CharLimit, ta.MaxHeight = 0, 0
	ta.SetValue(text)
	return ta.Value()
}

// ReplaceSelection removes complete selected objects and shifts surviving
// metadata once. Attachment synchronization belongs to the composer transaction.
func (i *Input) ReplaceSelection(text string) bool {
	return i.replaceSelection(text, nil)
}

// ReplaceSelectionWithTextPaste admits large text as one inline object and
// replaces the range in the same rebuild as an ordinary paste.
func (i *Input) ReplaceSelectionWithTextPaste(text string) bool {
	paste := newInlineLargePaste(text, i.nextPasteSeq+1)
	if paste == nil {
		return i.ReplaceSelection(text)
	}
	if !i.replaceSelection(paste.DisplayText, paste) {
		return false
	}
	i.nextPasteSeq++
	return true
}

func (i *Input) replaceSelection(text string, inserted *inlineLargePaste) bool {
	text = canonicalInputText(text)
	start, end, ok := i.SelectionRange()
	if !ok || !i.inlinePastesValid() {
		return false
	}
	runes := []rune(i.DisplayValue())
	replacement := []rune(text)
	kept := make([]inlineLargePaste, 0, len(i.inlinePastes))
	delta := len(replacement) - (end - start)
	for _, paste := range i.inlinePastes {
		if start < paste.End && end > paste.Start {
			continue
		}
		if paste.Start >= end {
			paste.Start += delta
			paste.End += delta
		}
		kept = append(kept, paste)
	}
	if inserted != nil {
		inserted.Start, inserted.End = start, start+len(replacement)
		index := len(kept)
		for idx, paste := range kept {
			if paste.Start >= start {
				index = idx
				break
			}
		}
		kept = append(kept, inlineLargePaste{})
		copy(kept[index+1:], kept[index:])
		kept[index] = *inserted
	}
	result := make([]rune, 0, len(runes)+delta)
	result = append(result, runes[:start]...)
	result = append(result, replacement...)
	result = append(result, runes[end:]...)
	i.inlinePastes = kept
	i.rebuildDisplay(string(result), start+utf8.RuneCountInString(text))
	i.ClearSelection()
	return true
}
