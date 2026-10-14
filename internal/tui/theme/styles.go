package theme

import "github.com/keakon/lipgloss/v2"

// Styles is one complete, read-only palette and derived style snapshot.
// Build creates a new snapshot; consumers must not mutate it or its Palette.
// Lipgloss modifier methods return values, so local presentation adjustments do
// not alter the shared snapshot. Publication and render-cache invalidation are
// owned by the TUI event loop.
type Styles struct {
	Palette Palette

	ViewportLineStyle            lipgloss.Style
	HeaderStyle                  lipgloss.Style
	SplashStyle                  lipgloss.Style
	SplashAccentStyle            lipgloss.Style
	UserCardStyle                lipgloss.Style
	AssistantCardStyle           lipgloss.Style
	ThinkingCardStyle            lipgloss.Style
	CompactionSummaryCardStyle   lipgloss.Style
	FocusedCardStyle             lipgloss.Style
	PillStyle                    lipgloss.Style
	ToolCallStyle                lipgloss.Style
	ToolBlockStyle               lipgloss.Style
	ToolArgsStyle                lipgloss.Style
	ToolResultBoxStyle           lipgloss.Style
	ToolResultStyle              lipgloss.Style
	ToolResultExpandedStyle      lipgloss.Style
	ToolFieldConnectorStyle      lipgloss.Style
	ToolStatusSuccessStyle       lipgloss.Style
	ToolStatusErrorStyle         lipgloss.Style
	ToolStatusNeutralStyle       lipgloss.Style
	ParamKeyStyle                lipgloss.Style
	ParamValStyle                lipgloss.Style
	QuestionSelectedOptionStyle  lipgloss.Style
	DiffAddStyle                 lipgloss.Style
	DiffDelStyle                 lipgloss.Style
	DiffAddInlineStyle           lipgloss.Style
	DiffDelInlineStyle           lipgloss.Style
	DiffAddLineBgStyle           lipgloss.Style
	DiffDelLineBgStyle           lipgloss.Style
	DiffHunkStyle                lipgloss.Style
	LSPErrorStyle                lipgloss.Style
	LSPWarnStyle                 lipgloss.Style
	LSPInfoStyle                 lipgloss.Style
	LSPHintStyle                 lipgloss.Style
	ErrorStyle                   lipgloss.Style
	ErrorCardStyle               lipgloss.Style
	LabelStyle                   lipgloss.Style
	UserLabelStyle               lipgloss.Style
	AssistantLabelStyle          lipgloss.Style
	ToolLabelStyle               lipgloss.Style
	ThinkingLabelStyle           lipgloss.Style
	ErrorLabelStyle              lipgloss.Style
	CompactionDegradedLabelStyle lipgloss.Style
	CompactionSectionLabelStyle  lipgloss.Style
	ThinkingTranslationStyle     lipgloss.Style
	ThinkingTranslationRuleStyle lipgloss.Style
	MessageContentStyle          lipgloss.Style
	ThinkingContentStyle         lipgloss.Style
	ThinkingTitleStyle           lipgloss.Style
	RailUserStyle                lipgloss.Style
	RailAssistantStyle           lipgloss.Style
	RailToolStyle                lipgloss.Style
	RailThinkingStyle            lipgloss.Style
	RailErrorStyle               lipgloss.Style

	StatusBarStyle             lipgloss.Style
	StatusBarPathStyle         lipgloss.Style
	StatusHintStyle            lipgloss.Style
	StatusChipStyle            lipgloss.Style
	StatusChipWarnStyle        lipgloss.Style
	StatusChipErrorStyle       lipgloss.Style
	StatsTabLabelStyle         lipgloss.Style
	TabStyle                   lipgloss.Style
	TabActiveStyle             lipgloss.Style
	SidebarFocusedStyle        lipgloss.Style
	SidebarEntryStyle          lipgloss.Style
	SidebarTaskStyle           lipgloss.Style
	SidebarFileStyle           lipgloss.Style
	SidebarAddedStyle          lipgloss.Style
	SidebarRemovedStyle        lipgloss.Style
	ToastInfoStyle             lipgloss.Style
	ToastWarnStyle             lipgloss.Style
	ToastErrorStyle            lipgloss.Style
	InfoPanelStyle             lipgloss.Style
	InfoPanelBlock             lipgloss.Style
	InfoPanelLineBg            lipgloss.Style
	InfoPanelTitle             lipgloss.Style
	InfoPanelValue             lipgloss.Style
	GaugeEmpty                 lipgloss.Style
	GaugeFull                  lipgloss.Style
	GaugeWarning               lipgloss.Style
	GaugeCritical              lipgloss.Style
	InfoPanelDim               lipgloss.Style
	InfoPanelEditAddedStyle    lipgloss.Style
	InfoPanelEditRemovedStyle  lipgloss.Style
	InfoPanelAgentEntryStyle   lipgloss.Style
	InfoPanelAgentFocusedStyle lipgloss.Style
	InfoPanelAgentStatusStyle  lipgloss.Style

	SelectedStyle             lipgloss.Style
	InputPromptStyle          lipgloss.Style
	ModeInsertStyle           lipgloss.Style
	ModeNormalStyle           lipgloss.Style
	SeparatorStyle            lipgloss.Style
	DirectoryBorderStyle      lipgloss.Style
	DialogBodyStyle           lipgloss.Style
	DialogTitleStyle          lipgloss.Style
	DimStyle                  lipgloss.Style
	InputSeparatorStyle       lipgloss.Style
	InputSeparatorDimmedStyle lipgloss.Style
	InputBoxStyle             lipgloss.Style
	InputBoxDimmedStyle       lipgloss.Style
	ConfirmSeparatorStyle     lipgloss.Style
	ConfirmToolStyle          lipgloss.Style
	KeyHintStyle              lipgloss.Style
	DialogPrimaryStyle        lipgloss.Style
	DialogDangerStyle         lipgloss.Style
	DialogWarningStyle        lipgloss.Style
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

	// QuestionTimeoutStyle styles the timeout countdown line.
	QuestionTimeoutStyle lipgloss.Style
}
