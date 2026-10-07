package tui

import (
	"github.com/keakon/bubbles/v2/key"
	"github.com/keakon/bubbles/v2/textarea"
	tea "github.com/keakon/bubbletea/v2"
)

type inputSelectionKeys struct {
	forward, backward, wordForward, wordBackward key.Binding
	up, down, all, copy                          key.Binding
}

func disableTextareaSelection(km *textarea.KeyMap) inputSelectionKeys {
	keys := inputSelectionKeys{km.SelectCharacterForward, km.SelectCharacterBackward, km.SelectWordForward, km.SelectWordBackward, km.SelectLineUp, km.SelectLineDown, km.SelectAll, km.CopySelection}
	km.SelectCharacterForward.SetEnabled(false)
	km.SelectCharacterBackward.SetEnabled(false)
	km.SelectWordForward.SetEnabled(false)
	km.SelectWordBackward.SetEnabled(false)
	km.SelectLineUp.SetEnabled(false)
	km.SelectLineDown.SetEnabled(false)
	km.SelectAll.SetEnabled(false)
	km.CopySelection.SetEnabled(false)
	return keys
}

func (i *Input) handleSelectionKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	km := i.textarea.KeyMap
	keys := i.selectionKeys
	if key.Matches(msg, keys.copy) {
		if text := i.SelectionText(); text != "" {
			return writeClipboardCmd(text, "Selection copied to clipboard"), true
		}
		return nil, true
	}
	if key.Matches(msg, keys.all) {
		i.StartSelection(0)
		i.UpdateSelection(len([]rune(i.DisplayValue())))
		return nil, true
	}
	var move tea.Key
	selecting, forward := true, true
	switch {
	case key.Matches(msg, keys.forward):
		move = tea.Key{Code: tea.KeyRight}
	case key.Matches(msg, keys.backward):
		move, forward = tea.Key{Code: tea.KeyLeft}, false
	case key.Matches(msg, keys.wordForward):
		move = tea.Key{Code: tea.KeyRight, Mod: tea.ModAlt}
	case key.Matches(msg, keys.wordBackward):
		move, forward = tea.Key{Code: tea.KeyLeft, Mod: tea.ModAlt}, false
	case key.Matches(msg, keys.up):
		move, forward = tea.Key{Code: tea.KeyUp}, false
	case key.Matches(msg, keys.down):
		move = tea.Key{Code: tea.KeyDown}
	default:
		selecting = false
	}
	if selecting {
		if !i.selection.active {
			i.StartSelection(runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column()))
		}
		original := i.textarea.KeyMap
		i.textarea.KeyMap.LinePrevious.SetKeys("up")
		i.textarea.KeyMap.LineNext.SetKeys("down")
		var cmd tea.Cmd
		i.textarea, cmd = i.textarea.Update(tea.KeyPressMsg(move))
		i.textarea.KeyMap = original
		offset := runeOffsetFromRowCol(i.DisplayValue(), i.Line(), i.Column())
		for _, paste := range i.inlinePastes {
			if offset > paste.Start && offset < paste.End {
				if forward {
					offset = paste.End
				} else {
					offset = paste.Start
				}
				break
			}
		}
		i.UpdateSelection(i.graphemeBoundary(offset, forward))
		return cmd, true
	}
	if !i.HasSelection() {
		return nil, false
	}
	if key.Matches(msg, km.CharacterBackward, km.CharacterForward) {
		start, end, _ := i.SelectionRange()
		if key.Matches(msg, km.CharacterBackward) {
			i.setCursorRuneOffset(start)
		} else {
			i.setCursorRuneOffset(end)
		}
		i.ClearSelection()
		return nil, true
	}
	if key.Matches(msg, km.DeleteCharacterBackward, km.DeleteCharacterForward, km.DeleteWordBackward, km.DeleteWordForward, km.DeleteBeforeCursor, km.DeleteAfterCursor) {
		return nil, i.ReplaceSelection("")
	}
	if key.Matches(msg, km.InsertNewline) {
		return nil, i.ReplaceSelection("\n")
	}
	if isComposerTextKey(msg) {
		return nil, i.ReplaceSelection(msg.Key().Text)
	}
	return nil, false
}

func isComposerTextKey(msg tea.KeyMsg) bool {
	k := msg.Key()
	return k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModMeta|tea.ModHyper) == 0
}
