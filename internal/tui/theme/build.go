package theme

import "github.com/keakon/lipgloss/v2"

func (s *Styles) applyBlockStyles(t Palette) {
	s.ViewportLineStyle = lipgloss.NewStyle().Background(lipgloss.Color(""))
	baseCardStyle := transcriptCardStyle()

	s.HeaderStyle = lipgloss.NewStyle().
		Bold(true).
		Background(lipgloss.Color(t.HeaderBg)).
		Foreground(lipgloss.Color(t.HeaderFg)).
		Padding(0, 1)

	s.SplashStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.SplashFg))

	s.SplashAccentStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.SplashAccentFg))

	s.UserCardStyle = baseCardStyle.
		Background(lipgloss.Color(t.UserCardBg))

	s.AssistantCardStyle = baseCardStyle.
		Background(lipgloss.Color(t.AssistantCardBg))

	s.ThinkingCardStyle = baseCardStyle.
		Background(lipgloss.Color(t.ThinkingCardBg)).
		Foreground(lipgloss.Color(t.ThinkingCardFg))

	s.CompactionSummaryCardStyle = baseCardStyle.
		Background(lipgloss.Color(t.CompactionSummaryBg))

	// Role badges (USER / ASSISTANT / THINKING / TOOL CALL) at the top-left of
	// message cards. Regressed when styles moved into ApplyTheme without these
	// assignments (63555b2).
	s.LabelStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	s.UserLabelStyle = s.LabelStyle.
		Background(lipgloss.Color(t.UserLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	s.AssistantLabelStyle = s.LabelStyle.
		Background(lipgloss.Color(t.AssistantLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	s.ToolLabelStyle = s.LabelStyle.
		Background(lipgloss.Color(t.ToolLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	s.ThinkingLabelStyle = s.LabelStyle.
		Background(lipgloss.Color(t.ThinkingLabelBg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	// The error card carries the same badge as every other card; it borrows the
	// error rail colour so the badge and the rail beside it read as one signal.
	s.ErrorLabelStyle = s.LabelStyle.
		Background(lipgloss.Color(t.RailErrorFg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	// A degraded compaction checkpoint (a structured fallback digest, or a
	// truncate-only archive with no summary) keeps the same badge as every
	// other summary - so the separator sits inside it with a space on either
	// side - and carries the warning in the badge surface itself, the way the
	// error card's badge borrows its rail colour. The badge ink stays the
	// shared dark label colour, which keeps the mode legible on the warning
	// background.
	s.CompactionDegradedLabelStyle = s.LabelStyle.
		Background(lipgloss.Color(t.InfoPanelDiagWarnFg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	// Section labels sit inside the checkpoint card, so they are plain emphasis
	// rather than a badge: a second badge style would read as a nested card.
	s.CompactionSectionLabelStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.DimFg))

	s.FocusedCardStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Background(lipgloss.Color(t.FocusedCardBg)).
		MarginBottom(1)

	s.PillStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Margin(0, 1).
		Foreground(lipgloss.Color(t.HeaderFg))

	s.ToolCallStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ToolCallFg))

	// Tool cards share the transcript card's padding and outer margin so
	// consecutive same-color tool calls remain visually distinct.
	s.ToolBlockStyle = baseCardStyle.
		PaddingRight(2).
		Background(lipgloss.Color(t.ToolCallBg))

	s.ToolArgsStyle = lipgloss.NewStyle().Padding(0, 0)
	s.ToolResultBoxStyle = lipgloss.NewStyle().Padding(0, 0)

	s.ToolResultStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ToolResultFg))

	s.ToolResultExpandedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ToolResultExpandedFg))

	// ToolFieldConnectorStyle renders the "↳" that opens a field row. It is
	// never bold and sits a few steps below the label's foreground, so a row
	// reads as connector -> label -> value instead of one flat grey run.
	s.ToolFieldConnectorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ToolFieldConnectorFg))

	// Emphasises the option the user picked inside a Question tool card. Reuses
	// the theme's "selected" foreground but deliberately not SelectedStyle:
	// that carries a background fill, and a card row must not gain a surface of
	// its own or the background recovery around it starts to leak. Bold plus the
	// brighter foreground is what separates the pick from the dimmed
	// alternatives, since DimFg and ToolResultFg are the same index here.
	s.QuestionSelectedOptionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.SelectedFg))

	s.ToolStatusSuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelSuccessFg))
	s.ToolStatusErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.ErrorFg))
	s.ToolStatusNeutralStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DimFg))

	// LSP diagnostic lines under Write/Edit tool results; palette matches info panel.
	s.LSPErrorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagErrorFg))
	s.LSPWarnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagWarnFg))
	s.LSPInfoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagInfoFg))
	s.LSPHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.InfoPanelDiagHintFg))

	s.ParamKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.ParamKeyFg))
	s.ParamValStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.ParamValFg))

	s.DiffAddStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DiffAddFg))
	s.DiffDelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DiffDelFg))
	s.DiffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DiffHunkFg))

	s.DiffAddInlineStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffAddInlineBg))
	s.DiffDelInlineStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffDelInlineBg))
	s.DiffAddLineBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffAddLineBg))
	s.DiffDelLineBgStyle = lipgloss.NewStyle().Background(lipgloss.Color(t.DiffDelLineBg))

	// Card rail (left-side colored border)
	s.RailUserStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailUserFg))
	s.RailAssistantStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailAssistantFg))
	s.RailToolStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailToolFg))
	s.RailThinkingStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailThinkingFg))
	s.RailErrorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.RailErrorFg))

	s.ErrorStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ErrorFg))

	s.ErrorCardStyle = baseCardStyle.
		Background(lipgloss.Color(t.ErrorCardBg)).
		Foreground(lipgloss.Color(t.ErrorCardFg))

	// ThinkingContentStyle is for assistant thinking/reasoning lines embedded
	// in assistant blocks. Use the theme's dim/secondary colour so it stays
	// visually distinct from the main reply across terminals. Italic adds
	// emphasis without relying on character-line decorations.
	s.ThinkingContentStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ThinkingCardFg)).
		Italic(true)

	// ThinkingTitleStyle highlights the first non-empty line in a thinking
	// section (often a short heading-like sentence). Title remains bold only
	// to avoid "bold+italic" overload.
	s.ThinkingTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ThinkingCardFg))

	s.ThinkingTranslationStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ThinkingCardFg))

	s.ThinkingTranslationRuleStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SeparatorFg))
}

func transcriptCardStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Padding(1, 2, 1, 1).
		MarginLeft(1).
		MarginBottom(1)
}

func (s *Styles) applyPanelStyles(t Palette) {
	s.StatusBarStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.StatusFg)).
		Height(1)
	s.StatusBarPathStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DimFg))
	s.StatusHintStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.StatusFg))

	// Status bar switches (LOOP / YOLO / MEMORY / PERSIST-FAIL) render as
	// chips so each one reads as a single token; the rest of the status text
	// stays foreground-only. The warning and failure chips reuse the badge
	// syntax (bright fill, shared dark ink, bold) already used by the error
	// card and the degraded-compaction badge.
	s.StatusChipStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Background(lipgloss.Color(t.StatusBg)).
		Foreground(lipgloss.Color(t.StatusFg))
	s.StatusChipWarnStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Bold(true).
		Background(lipgloss.Color(t.InfoPanelDiagWarnFg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))
	s.StatusChipErrorStyle = lipgloss.NewStyle().
		Padding(0, 1).
		Bold(true).
		Background(lipgloss.Color(t.ErrorFg)).
		Foreground(lipgloss.Color(t.LabelBadgeFg))

	s.StatsTabLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(t.StatsTabLabelFg))
	s.TabStyle = lipgloss.NewStyle().Padding(0, 1).Foreground(lipgloss.Color(t.StatsTabFg)).Background(lipgloss.Color(t.StatsTabBg))
	s.TabActiveStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color(t.SelectedFg)).Background(lipgloss.Color(t.SelectedBg))

	s.SidebarFocusedStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.SidebarFocusedFg)).
		Background(lipgloss.Color(t.SidebarFocusedBg))

	s.SidebarEntryStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarEntryFg))

	s.SidebarTaskStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarTaskFg))

	s.SidebarFileStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarTaskFg))

	s.SidebarAddedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarAddedFg))

	s.SidebarRemovedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SidebarRemovedFg))

	infoPanelBg := lipgloss.Color(t.InfoPanelBg)
	s.InfoPanelStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Padding(0, 1)

	s.InfoPanelBlock = lipgloss.NewStyle().
		Background(infoPanelBg)

	// Full-width line background only; use .Width(panelContentWidth).Render(line) so each line fills panel width.
	s.InfoPanelLineBg = lipgloss.NewStyle().Background(infoPanelBg)

	s.InfoPanelTitle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.DimFg)).
		MarginBottom(0)

	s.InfoPanelValue = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.HeaderFg)).
		Bold(true)

	s.GaugeEmpty = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.DimFg))
	s.GaugeFull = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.InfoPanelSuccessFg))
	s.GaugeWarning = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.InfoPanelWarningFg))
	s.GaugeCritical = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.InfoPanelCriticalFg))

	s.InfoPanelDim = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.DimFg))

	s.InfoPanelEditAddedStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarAddedFg))
	s.InfoPanelEditRemovedStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarRemovedFg))
	s.InfoPanelAgentEntryStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarEntryFg))
	s.InfoPanelAgentFocusedStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarFocusedFg)).
		Bold(true)
	s.InfoPanelAgentStatusStyle = lipgloss.NewStyle().
		Background(infoPanelBg).
		Foreground(lipgloss.Color(t.SidebarStatusFg))

	s.ToastInfoStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.ToastInfoBg)).
		Foreground(lipgloss.Color(t.ToastInfoFg)).
		Padding(0, 1)

	s.ToastWarnStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.ToastWarnBg)).
		Foreground(lipgloss.Color(t.ToastWarnFg)).
		Padding(0, 1)

	s.ToastErrorStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.ToastErrorBg)).
		Foreground(lipgloss.Color(t.ToastErrorFg)).
		Padding(0, 1)
}

func (s *Styles) applyDialogStyles(t Palette) {
	s.SelectedStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.SelectedBg)).
		Foreground(lipgloss.Color(t.SelectedFg))

	s.InputPromptStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.InputPromptFg))

	s.ModeInsertStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeInsertFg))

	s.ModeNormalStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeNormalFg))

	s.SeparatorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SeparatorFg))

	s.DirectoryBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(t.DirectoryBorderFg)).
		Background(lipgloss.Color(t.DialogBg)).
		Padding(0, 1)

	s.DialogBodyStyle = lipgloss.NewStyle().
		Background(lipgloss.Color(t.DialogBg))

	s.DialogTitleStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.DirectoryBorderFg))

	s.DimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.DimFg))

	// Keyboard hints are quiet navigation aids; selection surfaces belong to focus.
	s.KeyHintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(t.ConfirmToolFg))

	s.InputSeparatorStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ModeSearchFg))

	s.InputSeparatorDimmedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.SeparatorFg))

	s.InputBoxStyle = lipgloss.NewStyle()

	s.InputBoxDimmedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DimFg))

	s.ConfirmSeparatorStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ConfirmSeparatorFg))

	s.ConfirmToolStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.ConfirmToolFg))

	s.DialogPrimaryStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.DialogPrimaryFg))

	s.DialogDangerStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DialogDangerFg))

	s.DialogWarningStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(t.DialogWarningFg))

	s.ModeConfirmStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeConfirmFg))

	s.ModeQuestionStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeQuestionFg))

	s.ModeSearchStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeSearchFg))

	s.ModeModelSelectStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.ModeModelSelectFg))

	s.RolePlanStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color(t.RolePlanFg))
}

func (s *Styles) applyAliasStyles() {
	// Derive aliases after their source styles so every snapshot is complete.
	s.QuestionSeparatorStyle = s.ConfirmSeparatorStyle
	s.QuestionTextStyle = s.ConfirmToolStyle
	s.QuestionSelectedStyle = s.SelectedStyle
	s.QuestionTimeoutStyle = s.DialogWarningStyle

	// Search styles use the same snapshot as their source palette and text styles.
	s.SearchMatchStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color(s.Palette.SearchMatchFg)).
		Background(lipgloss.Color(s.Palette.SearchMatchBg)).
		Bold(true).
		Underline(true)
	s.SearchPromptStyle = s.DimStyle.Bold(true)
}

// Build derives styles in block, panel, dialog and alias order. It does not
// publish global state or invalidate renderers; the owner applies the snapshot
// and invalidates all of its theme-dependent caches together.
func Build(p Palette) *Styles {
	s := &Styles{Palette: p}
	s.applyBlockStyles(p)
	s.applyPanelStyles(p)
	s.applyDialogStyles(p)
	s.applyAliasStyles()
	return s
}
