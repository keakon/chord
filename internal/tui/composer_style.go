package tui

import (
	"github.com/keakon/bubbles/v2/textarea"
	"github.com/keakon/lipgloss/v2"
)

func newTextareaStyles() textarea.Styles {
	s := textarea.DefaultDarkStyles()
	s.Focused.Prompt = currentStyles.InputPromptStyle
	s.Blurred.Prompt = currentStyles.InputPromptStyle
	// Remove the default cursor-line background so the input area blends with the app background.
	s.Focused.CursorLine = lipgloss.NewStyle()
	return s
}
