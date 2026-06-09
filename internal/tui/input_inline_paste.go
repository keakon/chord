package tui

import (
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/message"
)

func (i *Input) DisplayValue() string {
	return i.textarea.Value()
}

func (i *Input) HasInlinePastes() bool {
	return len(i.inlinePastes) > 0
}

func (i *Input) InlinePasteRawContents() []string {
	if len(i.inlinePastes) == 0 {
		return nil
	}
	out := make([]string, 0, len(i.inlinePastes))
	for _, paste := range i.inlinePastes {
		out = append(out, paste.RawContent)
	}
	return out
}

func (i *Input) ContentParts() []message.ContentPart {
	return contentPartsWithInlinePastes(i.DisplayValue(), i.inlinePastes)
}

func (i *Input) inlinePastesValid() bool {
	if len(i.inlinePastes) == 0 {
		return true
	}
	runes := []rune(i.DisplayValue())
	prevEnd := 0
	for _, paste := range i.inlinePastes {
		if paste.Start < prevEnd || paste.End < paste.Start || paste.End > len(runes) {
			return false
		}
		if string(runes[paste.Start:paste.End]) != paste.DisplayText {
			return false
		}
		prevEnd = paste.End
	}
	return true
}

func (i *Input) rebuildDisplay(display string, cursor int) {
	i.textarea.SetValue(display)
	row, col := rowColFromRuneOffset(display, cursor)

	// Restore cursor after a SetValue() rebuild.
	//
	// CursorDown moves by visual (soft-wrapped) rows. Instead of trying to map a
	// logical row to a visual row (which depends on wrap width), we advance until
	// textarea's logical line index matches the target. We include a conservative
	// guard to prevent accidental infinite loops if upstream cursor movement rules
	// ever change.
	i.textarea.MoveToBegin()
	guard := 0
	for i.textarea.Line() < row {
		prevLine := i.textarea.Line()
		i.textarea.CursorDown()
		guard++
		if guard > 10000 {
			break
		}
		// If we didn't advance the logical line, we are still walking soft-wrapped
		// rows within the same logical line; continue.
		if i.textarea.Line() == prevLine {
			continue
		}
	}
	// If we bailed out via guard, fall back to end-of-input rather than leaving
	// the cursor at the top.
	if i.textarea.Line() < row {
		i.textarea.MoveToEnd()
	}
	i.textarea.SetCursorColumn(col)
}

func (i *Input) RemoveInlinePasteAtCursor() (inlineLargePaste, bool) {
	if len(i.inlinePastes) == 0 {
		return inlineLargePaste{}, false
	}
	cursor := runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column())
	idx := -1
	for j, paste := range i.inlinePastes {
		if cursor > paste.Start && cursor <= paste.End {
			idx = j
			break
		}
	}
	if idx < 0 {
		return inlineLargePaste{}, false
	}
	return i.removeInlinePaste(idx), true
}

func (i *Input) RemoveInlinePasteForwardAtCursor() (inlineLargePaste, bool) {
	if len(i.inlinePastes) == 0 {
		return inlineLargePaste{}, false
	}
	cursor := runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column())
	idx := -1
	for j, paste := range i.inlinePastes {
		if cursor >= paste.Start && cursor < paste.End {
			idx = j
			break
		}
	}
	if idx < 0 {
		return inlineLargePaste{}, false
	}
	return i.removeInlinePaste(idx), true
}

func (i *Input) removeInlinePaste(idx int) inlineLargePaste {
	if idx < 0 || idx >= len(i.inlinePastes) {
		return inlineLargePaste{}
	}
	paste := i.inlinePastes[idx]
	runes := []rune(i.DisplayValue())
	runes = append(runes[:paste.Start], runes[paste.End:]...)
	delta := paste.End - paste.Start
	for j := idx + 1; j < len(i.inlinePastes); j++ {
		i.inlinePastes[j].Start -= delta
		i.inlinePastes[j].End -= delta
	}
	i.inlinePastes = append(i.inlinePastes[:idx], i.inlinePastes[idx+1:]...)
	i.rebuildDisplay(string(runes), paste.Start)
	i.ClearSelection()
	return paste
}

func (i *Input) ProtectInlinePastesOnKey(msg tea.KeyMsg) bool {
	if len(i.inlinePastes) == 0 {
		return false
	}
	cursor := runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column())
	s := msg.String()
	for _, paste := range i.inlinePastes {
		switch s {
		case "left", "ctrl+b":
			if cursor == paste.End {
				i.rebuildDisplay(i.DisplayValue(), paste.Start)
				return true
			}
		case "right", "ctrl+f":
			if cursor == paste.Start {
				i.rebuildDisplay(i.DisplayValue(), paste.End)
				return true
			}
		case "home", "ctrl+a":
			if cursor > paste.Start && cursor < paste.End {
				i.rebuildDisplay(i.DisplayValue(), paste.Start)
				return true
			}
		case "end", "ctrl+e":
			if cursor > paste.Start && cursor < paste.End {
				i.rebuildDisplay(i.DisplayValue(), paste.End)
				return true
			}
		default:
			if msg.Key().Text != "" {
				if cursor > paste.Start && cursor < paste.End {
					i.rebuildDisplay(i.DisplayValue(), paste.Start)
					return true
				}
			}
		}
	}
	return false
}

func (i *Input) insertInlinePaste(paste *inlineLargePaste) bool {
	if paste == nil {
		return false
	}
	display := i.DisplayValue()
	cursor := runeOffsetFromRowCol(display, i.Line(), i.Column())
	displayRunes := []rune(display)
	insertRunes := []rune(paste.DisplayText)
	newRunes := make([]rune, 0, len(displayRunes)+len(insertRunes))
	newRunes = append(newRunes, displayRunes[:cursor]...)
	newRunes = append(newRunes, insertRunes...)
	newRunes = append(newRunes, displayRunes[cursor:]...)
	for j := range i.inlinePastes {
		if i.inlinePastes[j].Start >= cursor {
			i.inlinePastes[j].Start += len(insertRunes)
			i.inlinePastes[j].End += len(insertRunes)
		}
	}
	paste.Start = cursor
	paste.End = cursor + len(insertRunes)
	insertAt := len(i.inlinePastes)
	for j, existing := range i.inlinePastes {
		if cursor < existing.Start {
			insertAt = j
			break
		}
	}
	i.inlinePastes = append(i.inlinePastes, inlineLargePaste{})
	copy(i.inlinePastes[insertAt+1:], i.inlinePastes[insertAt:])
	i.inlinePastes[insertAt] = *paste
	i.rebuildDisplay(string(newRunes), paste.End)
	i.ClearSelection()
	return true
}

func (i *Input) InsertLargePaste(content string) bool {
	paste := newInlineLargePaste(content, i.nextPasteSeq+1)
	if paste == nil {
		return false
	}
	i.nextPasteSeq++
	return i.insertInlinePaste(paste)
}

func (i *Input) InsertImagePlaceholder(index int) bool {
	return i.insertInlinePaste(newInlineImagePlaceholder(index))
}

func (i *Input) InsertStringPreserveInlinePastes(s string) bool {
	if len(i.inlinePastes) == 0 {
		i.InsertString(s)
		return true
	}
	cursor := runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column())
	return i.ReplaceRuneRangePreserveInlinePastes(cursor, cursor, s)
}

func (i *Input) RemoveInlineImagePlaceholderByRaw(raw string) bool {
	if raw == "" || len(i.inlinePastes) == 0 {
		return false
	}
	for idx, paste := range i.inlinePastes {
		if paste.Kind == inlineTokenImage && paste.RawContent == raw {
			i.removeInlinePaste(idx)
			return true
		}
	}
	return false
}

func (i *Input) ReindexInlineImagePlaceholdersAfterRemoval(removedIndex int) {
	if removedIndex < 1 || len(i.inlinePastes) == 0 {
		return
	}
	for idx := range i.inlinePastes {
		if i.inlinePastes[idx].Kind != inlineTokenImage {
			continue
		}
		imageIndex, ok := inlineImagePlaceholderIndex(i.inlinePastes[idx].RawContent)
		if !ok || imageIndex <= removedIndex {
			continue
		}
		i.inlinePastes[idx].RawContent = imagePlaceholder(imageIndex - 1)
	}
}

func (i *Input) ReindexInlineImagePlaceholders(mapping map[int]int) {
	if len(mapping) == 0 || len(i.inlinePastes) == 0 {
		return
	}
	for idx := range i.inlinePastes {
		if i.inlinePastes[idx].Kind != inlineTokenImage {
			continue
		}
		imageIndex, ok := inlineImagePlaceholderIndex(i.inlinePastes[idx].RawContent)
		if !ok {
			continue
		}
		newIndex, ok := mapping[imageIndex]
		if !ok {
			continue
		}
		i.inlinePastes[idx].RawContent = imagePlaceholder(newIndex)
	}
}

func (i *Input) SetDisplayValueAndPastes(display string, pastes []inlineLargePaste, nextSeq int) {
	i.SetValue(display)
	i.inlinePastes = copyInlineLargePastes(pastes)
	if nextSeq < 0 {
		nextSeq = 0
	}
	i.nextPasteSeq = nextSeq
}

func (i *Input) ensureCursorOutsideInlinePastes() {
	if len(i.inlinePastes) == 0 {
		return
	}
	cursor := runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column())
	for _, paste := range i.inlinePastes {
		if cursor > paste.Start && cursor < paste.End {
			i.rebuildDisplay(i.DisplayValue(), paste.End)
			return
		}
	}
}

func (i *Input) SetInlinePastes(pastes []inlineLargePaste, nextSeq int) {
	i.inlinePastes = copyInlineLargePastes(pastes)
	if nextSeq < 0 {
		nextSeq = 0
	}
	i.nextPasteSeq = nextSeq
}

func (i *Input) InlinePastes() []inlineLargePaste {
	return copyInlineLargePastes(i.inlinePastes)
}

func (i *Input) NextPasteSeq() int {
	return i.nextPasteSeq
}
