package tui

import "github.com/keakon/bubbles/v2/textarea"

const dialogTextareaMaxContentHeight = 10000

func newDialogTextarea(width, minHeight, maxHeight int, value string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = minHeight
	ta.MaxHeight = maxHeight
	ta.MaxContentHeight = dialogTextareaMaxContentHeight
	ta.SetStyles(newTextareaStyles())
	ta.SetPromptFunc(0, func(textarea.PromptInfo) string {
		return ""
	})
	km := ta.KeyMap
	km.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	ta.KeyMap = km
	configureDialogTextarea(&ta, width, minHeight, maxHeight)
	ta.SetValue(value)
	ta.CursorEnd()
	ta.Focus()
	return ta
}

func configureDialogTextarea(ta *textarea.Model, width, minHeight, maxHeight int) {
	ta.MinHeight = minHeight
	ta.MaxHeight = maxHeight
	ta.DynamicHeight = true
	ta.MaxContentHeight = dialogTextareaMaxContentHeight
	ta.SetWidth(width)
}
