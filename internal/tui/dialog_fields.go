package tui

// Field focus is a position cue; it does not imply permission or approval.
func renderDialogInputField(view string, focused bool) string {
	if focused {
		return SelectedStyle.Render("> ") + view
	}
	return "  " + view
}
