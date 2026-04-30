package tui

import "charm.land/lipgloss/v2"

func init() {
	ApplyTheme(DefaultTheme())
}

// ApplyTheme rebuilds all package-level styles from the given Theme.
func ApplyTheme(t Theme) {
	currentTheme = t
	applyBlockStyles(t)
	applyPanelStyles(t)
	applyDialogStyles(t)
	applyAliasStyles()
	resetMarkdownRenderer()
}

func applyBlockStyles(t Theme) {
	ViewportLineStyle = lipgloss.NewStyle().Background(lipgloss.Color(""))

	HeaderStyle = lipgloss.NewStyle().
		Bold(true).
		Background(lipgloss.Color(t.HeaderBg)).
		Foreground(lipgloss.Color(t.HeaderFg)).
		Padding(0, 1)

	UserCardStyle = lipgloss.NewStyle().
		Padding(1, 1).
		MarginLeft(1).
		MarginBottom(1).
		Background(lipgloss.Color(t.UserCardBg))

	AssistantCardStyle = lipgloss.NewStyle().
		Padding(1, 1).
		MarginLeft(1).
		MarginBottom(1).
		Background(lipgloss.Color(t.AssistantCardBg))

	// Thinking blocks use extra left padding to visually nest them as
	// "inner dialogue" compared to primary user/assistant cards.
	ThinkingCardStyle = lipgloss.NewStyle().
		Padding(1, 1).
		PaddingLeft(2).
		MarginLeft(1).
		MarginBottom(1).
		Background(lipgloss.Color(t.ThinkingCardBg)).
		Foreground(lipgloss.Color(t.ThinkingCardFg))

	CompactionSummaryCardStyle = lipgloss.NewStyle().
		Padding(1, 1).
		MarginLeft(1).
		MarginBottom(1).
		Background(lipgloss.Color(t.CompactionSummaryBg))

	// Role badges (USER / ASSISTANT / THINKING / TOOL CALL) at the top-left of
	// message cards. Regressed when styles moved into ApplyTheme without these
	// assignments (63555b2).
	LabelStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	UserLabelStyle = LabelStyle.
		Background(lipgloss.Color(t.UserLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	AssistantLabelStyle = LabelStyle.
		Background(lipgloss.Color(t.AssistantLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	ToolLabelStyle = LabelStyle.
		Background(lipgloss.Color(t.ToolLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	ThinkingLabelStyle = LabelStyle.
		Background(lipgloss.Color(t.ThinkingLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))

	ThinkingMarginStyle = lipgloss.NewStyle().PaddingLeft(2)

	FocusedCardStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Background(lipgloss.Color(t.FocusedCardBg)).
		MarginBottom(1)

	PillStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Margin(0, 1).
		Foreground(lipgloss.Color(t.HeaderFg))

	ToolCallStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ToolCallFg))

	ToolBlockStyle = lipgloss.NewStyle().
		Padding(1, 2, 1, 1).
		MarginLeft(1).
		MarginBottom(1).
		Background(lipgloss.Color(t.ToolCallBg))

	ToolArgsStyle = lipgloss.NewStyle().Padding(0, 0)
	ToolResultBoxStyle = lipgloss.NewStyle().Padding(0, 0)

	ToolResultStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ToolResultFg))

	ToolResultExpandedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ToolResultExpandedFg))

	// LSP diagnostic lines under Write/Edit tool results; palette matches info panel.
	LSPErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagErrorFg))
	LSPWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagWarnFg))
	LSPInfoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagInfoFg))
	LSPHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagHintFg))

	paramKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.ParamKeyFg))
	paramValStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.ParamValFg))

	DiffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DiffAddFg))
	DiffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DiffDelFg))
	DiffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DiffHunkFg))

	DiffAddInlineStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffAddInlineBg))
	DiffDelInlineStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffDelInlineBg))
	DiffAddLineBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffAddLineBg))
	DiffDelLineBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffDelLineBg))
	diffAddBg = t.DiffAddLineBg
	diffDelBg = t.DiffDelLineBg

	// Card rail (left-side colored border)
	RailUserStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailUserFg))
	RailAssistantStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailAssistantFg))
	RailToolStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailToolFg))
	RailThinkingStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailThinkingFg))
	RailErrorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailErrorFg))

	ErrorStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ErrorFg))

	ErrorCardStyle = lipgloss.NewStyle().
		Padding(1, 1).
		MarginLeft(1).
		MarginBottom(1).
		Background(lipgloss.Color(t.ErrorCardBg)).
		Foreground(lipgloss.Color(t.ErrorCardFg))

	// ThinkingContentStyle is for assistant thinking/reasoning lines embedded
	// in assistant blocks. Use the theme's dim/secondary colour so it stays
	// visually distinct from the main reply across terminals. Italic adds
	// emphasis without relying on character-line decorations.
	ThinkingContentStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DimFg)).
		Italic(true)

	// ThinkingTitleStyle highlights the first non-empty line in a thinking
	// section (often a short heading-like sentence). Title remains bold only
	// to avoid "bold+italic" overload.
	ThinkingTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ThinkingCardFg))
}

func applyPanelStyles(t Theme) {
	StatusBarStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.StatusFg)).
		Height(1)
	StatusBarPathStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DimFg))
	StatusHintStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.StatusFg))

	StatsTabLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.StatsTabLabelFg))
	StatsTabStyle = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color(t.StatsTabFg)).Background(lipgloss.Color(t.StatsTabBg))
	StatsTabActiveStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(t.StatsTabActiveFg)).Background(lipgloss.Color(t.StatsTabActiveBg))

	SidebarFocusedStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.SidebarFocusedFg)).
		Background(lipgloss.Color(t.SidebarFocusedBg))

	SidebarEntryStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarEntryFg))

	SidebarTaskStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarTaskFg))

	SidebarStatusStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarStatusFg))

	SidebarFileStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarTaskFg))

	SidebarAddedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarAddedFg))

	SidebarRemovedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarRemovedFg))

	infoPanelBg := lipgloss.Color(t.InfoPanelBg)
	InfoPanelStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Padding(0, 1)

	InfoPanelBlock = lipgloss.NewStyle().
		Background(infoPanelBg)

	// Full-width line background only; use .Width(panelContentWidth).Render(line) so each line fills panel width.
	InfoPanelLineBg = lipgloss.NewStyle().Background(infoPanelBg)

	InfoPanelTitle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.DimFg)).
		MarginBottom(0)

	InfoPanelValue = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.HeaderFg)).
		Bold(true)

	GaugeEmpty = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.DimFg))
	GaugeFull = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.InfoPanelSuccessFg))
	GaugeWarning = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.InfoPanelWarningFg))
	GaugeCritical = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.InfoPanelCriticalFg))

	InfoPanelDim = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.DimFg))

	InfoPanelEditAddedStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarAddedFg))
	InfoPanelEditRemovedStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarRemovedFg))
	InfoPanelAgentEntryStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarEntryFg))
	InfoPanelAgentFocusedStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarFocusedFg)).
		Bold(true)
	InfoPanelAgentStatusStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarStatusFg))

	ToastInfoStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.ToastInfoBg)).
		Foreground(lipgloss.Color(t.ToastInfoFg)).
		Padding(0, 1)

	ToastWarnStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.ToastWarnBg)).
		Foreground(lipgloss.Color(t.ToastWarnFg)).
		Padding(0, 1)

	ToastErrorStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.ToastErrorBg)).
		Foreground(lipgloss.Color(t.ToastErrorFg)).
		Padding(0, 1)
}

func applyDialogStyles(t Theme) {
	SelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.SelectedBg)).
		Foreground(lipgloss.Color(t.SelectedFg))

	InputPromptStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.InputPromptFg))

	ModeInsertStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeInsertFg))

	ModeNormalStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeNormalFg))

	SeparatorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SeparatorFg))

	DirectoryBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.DirectoryBorderFg)).
		Padding(0, 1)

	DialogTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.DirectoryBorderFg))

	DimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DimFg))

	InputSeparatorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ModeSearchFg))

	InputSeparatorDimmedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SeparatorFg))

	InputBoxStyle = lipgloss.NewStyle()

	InputBoxDimmedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DimFg))

	ConfirmSeparatorStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ConfirmSeparatorFg))

	ConfirmToolStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ConfirmToolFg))

	ConfirmHintStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ConfirmHintFg))

	ConfirmAllowStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ConfirmAllowFg))

	ConfirmDenyStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ConfirmDenyFg))

	ConfirmEditStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ConfirmEditFg))

	ModeConfirmStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeConfirmFg))

	ModeQuestionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeQuestionFg))

	ModeSearchStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeSearchFg))

	ModeModelSelectStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeModelSelectFg))

	RolePlanStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.RolePlanFg))
}

func applyAliasStyles() {
	// question.go sets Question*Style = Confirm* / SelectedStyle in a var block.
	// That copy runs before init()'s ApplyTheme, so without reassignment here the
	// aliases stay empty lipgloss styles (no cursor row highlight, wrong hint colours).
	QuestionSeparatorStyle = ConfirmSeparatorStyle
	QuestionTextStyle = ConfirmToolStyle
	QuestionSelectedStyle = SelectedStyle
	QuestionHintStyle = ConfirmHintStyle
	QuestionTimeoutStyle = ConfirmDenyStyle

	// search.go: same init-order trap as Question* (copy before ApplyTheme).
	SearchMatchStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.SearchMatchFg)).
		Background(lipgloss.Color(currentTheme.SearchMatchBg))
	SearchPromptStyle = DimStyle.Bold(true)
}
