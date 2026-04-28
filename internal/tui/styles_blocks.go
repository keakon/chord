package tui

import "charm.land/lipgloss/v2"

const (
	// SectionSeparator is the visual separator rune used by the animated input divider.
	SectionSeparator = "─"
	// ViewportBg is the single background colour for the entire chat content area.
	ViewportBg = ""
	// RailWidth is the width of the conversation rail (left-side colored border).
	RailWidth = 1
)

var (
	ViewportLineStyle          lipgloss.Style
	HeaderStyle                lipgloss.Style
	UserCardStyle              lipgloss.Style
	AssistantCardStyle         lipgloss.Style
	ThinkingCardStyle          lipgloss.Style
	CompactionSummaryCardStyle lipgloss.Style
	ThinkingMarginStyle        lipgloss.Style
	FocusedCardStyle           lipgloss.Style
	PillStyle                  lipgloss.Style
	ToolCallStyle              lipgloss.Style
	ToolBlockStyle             lipgloss.Style
	ToolArgsStyle              lipgloss.Style
	ToolResultBoxStyle         lipgloss.Style
	ToolResultStyle            lipgloss.Style
	ToolResultExpandedStyle    lipgloss.Style
	paramKeyStyle              lipgloss.Style
	paramValStyle              lipgloss.Style
	DiffAddStyle               lipgloss.Style
	DiffDelStyle               lipgloss.Style
	DiffAddInlineStyle         lipgloss.Style
	DiffDelInlineStyle         lipgloss.Style
	DiffAddLineBgStyle         lipgloss.Style
	DiffDelLineBgStyle         lipgloss.Style
	DiffHunkStyle              lipgloss.Style
	LSPErrorStyle              lipgloss.Style
	LSPWarnStyle               lipgloss.Style
	LSPInfoStyle               lipgloss.Style
	LSPHintStyle               lipgloss.Style
	ErrorStyle                 lipgloss.Style
	ErrorCardStyle             lipgloss.Style
	LabelStyle                 lipgloss.Style
	UserLabelStyle             lipgloss.Style
	AssistantLabelStyle        lipgloss.Style
	ToolLabelStyle             lipgloss.Style
	ThinkingLabelStyle         lipgloss.Style
	MessageContentStyle        lipgloss.Style
	ThinkingContentStyle       lipgloss.Style
	ThinkingTitleStyle         lipgloss.Style
	RailUserStyle              lipgloss.Style
	RailAssistantStyle         lipgloss.Style
	RailToolStyle              lipgloss.Style
	RailThinkingStyle          lipgloss.Style
	RailErrorStyle             lipgloss.Style
)
