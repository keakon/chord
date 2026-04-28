package tui

import "charm.land/lipgloss/v2"

var (
	SelectedStyle             lipgloss.Style
	InputPromptStyle          lipgloss.Style
	ModeInsertStyle           lipgloss.Style
	ModeNormalStyle           lipgloss.Style
	SeparatorStyle            lipgloss.Style
	DirectoryBorderStyle      lipgloss.Style
	DialogTitleStyle          lipgloss.Style
	DimStyle                  lipgloss.Style
	InputSeparatorStyle       lipgloss.Style
	InputSeparatorDimmedStyle lipgloss.Style
	InputBoxStyle             lipgloss.Style
	InputBoxDimmedStyle       lipgloss.Style
	ConfirmSeparatorStyle     lipgloss.Style
	ConfirmToolStyle          lipgloss.Style
	ConfirmHintStyle          lipgloss.Style
	ConfirmAllowStyle         lipgloss.Style
	ConfirmDenyStyle          lipgloss.Style
	ConfirmEditStyle          lipgloss.Style
	ModeConfirmStyle          lipgloss.Style
	ModeQuestionStyle         lipgloss.Style
	ModeSearchStyle           lipgloss.Style
	ModeModelSelectStyle      lipgloss.Style
	RolePlanStyle             lipgloss.Style

	// SearchPromptStyle styles the "/" prompt in the search input.
	// Uses the same dim colour as other secondary UI elements.
	SearchPromptStyle lipgloss.Style

	// SearchMatchStyle can be used to highlight matched blocks in the viewport.
	// This is provided for the integration layer to use when rendering.
	SearchMatchStyle lipgloss.Style

	// QuestionSeparatorStyle styles the top separator of the question dialog.
	QuestionSeparatorStyle lipgloss.Style

	// QuestionTextStyle styles the question text.
	QuestionTextStyle lipgloss.Style

	// QuestionSelectedStyle highlights the cursor-selected option.
	QuestionSelectedStyle lipgloss.Style

	// QuestionHintStyle styles the hint/key-binding line.
	QuestionHintStyle lipgloss.Style

	// QuestionTimeoutStyle styles the timeout countdown line.
	QuestionTimeoutStyle lipgloss.Style
)
