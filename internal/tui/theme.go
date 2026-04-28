package tui

// Theme holds colour definitions for all TUI elements (ANSI color names or hex).
// Changing a Theme and passing it to ApplyTheme updates every package-level lipgloss style.
// Lipgloss v2 uses lipgloss.Color(s) to convert these strings to colors in styles.
//
// Chord only ships a dark palette. A light variant existed historically but was
// removed after audit — it had lifelong collisions (user==tool surfaces,
// labelBadgeFg on pale badge bgs producing ~1.5:1 contrast, 1-step grey ladder
// invisible on white) that went unreported for years, which was the signal
// that no user actually ran it. If light-mode support is ever re-added, do it
// behind an explicit config flag plus a real audit, not an untested mirror of
// this file.
//
// Message card palette constraints (dark theme)
//
// The current UI uses a single background per card kind (resting surface).
// Focus indication is provided by the 1-column left rail, not by changing the
// full card background.
//
// When picking surface values, honour the following rules — they encode past
// bugs that were painful to debug:
//
//  1. No surface may collide with another surface. A collision makes adjacent
//     cards visually merge.
//
//  2. surfaceUser must NOT be ANSI 232 (#080808). Users running iTerm2's
//     default dark profile (pure black #000000) cannot distinguish 232 from
//     the terminal background and the USER card disappears. Start from 233
//     (#121212) at minimum. CodeBlockBg is allowed to sit at 232 because it
//     only renders inside other cards, never adjacent to the terminal
//     background.
//
//  3. InlineCodeBg / CodeBlockBg should remain visually distinct from the
//     common card surfaces they render inside so inline and fenced code never
//     "disappear" into the surrounding paragraph background.
//
//  4. CompactionSummaryBg intentionally reuses the assistant surface —
//     compaction summaries are rendered as assistant-shaped recaps. Do not
//     diverge them.
//
//  5. Values reused by non-card surfaces (InfoPanelBg, StatusBg, StatsTabBg,
//     SidebarFocusedBg, SelectedBg, etc.) may equal a card surface because
//     they live in separate screen regions; collisions there are cosmetic at
//     worst.
//
// If you ever re-introduce whole-card focus backgrounds, add explicit fields
// and tests for them. The current UI treats the rail as the only focus
// indicator.
type Theme struct {
	Name string

	// Header bar
	HeaderBg string
	HeaderFg string

	// Message labels
	UserFg      string
	AssistantFg string

	// Message cards
	UserCardBg      string
	AssistantCardBg string

	// Thinking block (reasoning / extended thinking)
	ThinkingCardBg      string
	ThinkingCardFg      string
	CompactionSummaryBg string

	// Tool blocks
	ToolCallFg           string
	ToolCallBg           string
	ToolResultFg         string
	ToolResultBg         string
	ToolResultExpandedFg string
	ParamKeyFg           string
	ParamValFg           string
	DiffAddFg            string
	DiffDelFg            string
	DiffHunkFg           string
	DiffAddLineBg        string
	DiffDelLineBg        string
	DiffAddInlineBg      string
	DiffDelInlineBg      string
	InlineCodeBg         string
	CodeBlockBg          string
	CodeBlockFg          string
	CodeBlockLabelFg     string

	// Errors
	ErrorFg     string
	ErrorCardBg string
	ErrorCardFg string

	// Labels / badges
	LabelBadgeFg     string
	UserLabelBg      string
	AssistantLabelBg string
	ToolLabelBg      string
	ThinkingLabelBg  string

	// Status / utility surfaces
	FocusedCardBg    string
	StatsTabLabelFg  string
	StatsTabFg       string
	StatsTabBg       string
	StatsTabActiveFg string
	StatsTabActiveBg string
	SidebarAddedFg   string
	SidebarRemovedFg string

	// Status bar
	StatusBg string
	StatusFg string

	// Mode badges (rendered on StatusBg)
	ModeInsertFg      string
	ModeNormalFg      string
	ModeConfirmFg     string
	ModeQuestionFg    string
	ModeSearchFg      string
	ModeModelSelectFg string // MODEL SELECT badge foreground
	RolePlanFg        string // PLAN role badge foreground

	// Search match highlight (gentler than full selected/reverse-video)
	SearchMatchFg string
	SearchMatchBg string

	// Selected item (directory overlay)
	SelectedBg string
	SelectedFg string

	// Input prompt
	InputPromptFg string

	// Separator
	SeparatorFg string

	// Directory border
	DirectoryBorderFg string

	// Dim / secondary text
	DimFg string

	// Confirmation dialog
	ConfirmSeparatorFg string
	ConfirmToolFg      string
	ConfirmHintFg      string
	ConfirmAllowFg     string
	ConfirmDenyFg      string
	ConfirmEditFg      string

	// Sidebar (multi-agent)
	SidebarBorderFg  string
	SidebarFocusedFg string
	SidebarFocusedBg string
	SidebarEntryFg   string
	SidebarTaskFg    string
	SidebarStatusFg  string

	// Info panel
	InfoPanelBg string

	// Info panel semantic colors
	InfoPanelSuccessFg      string
	InfoPanelWarningFg      string
	InfoPanelCriticalFg     string
	InfoPanelPendingFg      string
	InfoPanelDiagErrorFg    string
	InfoPanelDiagWarnFg     string
	InfoPanelDiagInfoFg     string
	InfoPanelDiagHintFg     string
	InfoPanelKeyWarnFg      string
	InfoPanelKeyCriticalFg  string
	InfoPanelRateWarnFg     string
	InfoPanelRateCriticalFg string

	// Animated accents
	AccentGradientFromFg string
	AccentGradientToFg   string
	StatusPulseFromFg    string
	StatusPulseToFg      string

	// Conversation rail (left-side colored border per card kind)
	RailUserFg             string
	RailAssistantFg        string
	RailToolFg             string
	RailThinkingFg         string
	RailErrorFg            string
	RailUserFocusedFg      string
	RailAssistantFocusedFg string
	RailToolFocusedFg      string
	RailThinkingFocusedFg  string
	RailErrorFocusedFg     string

	// Toast notifications
	ToastInfoBg  string
	ToastInfoFg  string
	ToastWarnBg  string
	ToastWarnFg  string
	ToastErrorBg string
	ToastErrorFg string
}

const defaultInfoPanelBg = "235"

// currentTheme stores the last theme applied via ApplyTheme.
// Used by rendering code to access theme color values for ANSI manipulation
// (e.g. preserveCardBg re-inserts background after inner resets).
var currentTheme Theme

// DefaultTheme returns the built-in dark theme matching the original
// hardcoded colours.
func DefaultTheme() Theme {
	// Card surface ladder (darkest → lightest).
	// surfaceUser is deliberately NOT 232: on iTerm2's default dark profile
	// (pure black #000000) 232 (#080808) is indistinguishable and the USER
	// card disappears. 233 (#121212) keeps enough contrast without losing the
	// "user is the calmest surface" layering.
	surfaceUser := "233"
	surfaceAssistant := "235"
	surfaceThinking := "237"
	surfaceTool := "236"
	surfacePanel := defaultInfoPanelBg
	surfaceStatus := "236"

	accentUserBadge := "65"
	accentAssistantBadge := "61"
	accentToolBadge := "30"
	accentThinkingBadge := "243"

	return Theme{
		Name:                    "dark",
		HeaderBg:                "63",
		HeaderFg:                "230",
		UserFg:                  "82",
		AssistantFg:             "69",
		UserCardBg:              surfaceUser,
		AssistantCardBg:         surfaceAssistant,
		CompactionSummaryBg:     surfaceAssistant,
		ThinkingCardBg:          surfaceThinking,
		ThinkingCardFg:          "251", // lighter so thinking text is readable on dark bg
		ToolCallFg:              "69",
		ToolCallBg:              surfaceTool,
		ToolResultFg:            "250", // lighter so tool result text is readable
		ToolResultBg:            "",
		ToolResultExpandedFg:    "252",
		ParamKeyFg:              "244",
		ParamValFg:              "252",
		DiffAddFg:               "78",
		DiffDelFg:               "167",
		DiffHunkFg:              "75",
		DiffAddLineBg:           "#1e3d2e",
		DiffDelLineBg:           "#3d2525",
		DiffAddInlineBg:         "#2d5a2d",
		DiffDelInlineBg:         "#5a2d2d",
		InlineCodeBg:            "239", // inline code only; keep distinct from common card surfaces so it reads as an inset surface
		CodeBlockBg:             "232", // darker than any card surface (user=233) so fenced blocks read as inset surfaces; only rendered inside cards, never adjacent to the terminal background
		CodeBlockFg:             "252",
		CodeBlockLabelFg:        "245",
		ErrorFg:                 "196",
		ErrorCardBg:             "52",
		ErrorCardFg:             "196",
		LabelBadgeFg:            "232",
		UserLabelBg:             accentUserBadge,      // desaturated olive green (was 82)
		AssistantLabelBg:        accentAssistantBadge, // desaturated blue-purple (was 69)
		ToolLabelBg:             accentToolBadge,      // desaturated cyan (was 37)
		ThinkingLabelBg:         accentThinkingBadge,  // medium gray (was 248)
		FocusedCardBg:           "24",
		StatsTabLabelFg:         "245",
		StatsTabFg:              "250",
		StatsTabBg:              "236",
		StatsTabActiveFg:        "230",
		StatsTabActiveBg:        "63",
		SidebarAddedFg:          "76",
		SidebarRemovedFg:        "167",
		StatusBg:                surfaceStatus,
		StatusFg:                "252",
		ModeInsertFg:            "82",
		ModeNormalFg:            "220",
		ModeConfirmFg:           "196",
		ModeQuestionFg:          "220",
		ModeSearchFg:            "69",
		ModeModelSelectFg:       "69",
		RolePlanFg:              "214",
		SearchMatchFg:           "230",
		SearchMatchBg:           "58",
		SelectedBg:              "62",
		SelectedFg:              "230",
		InputPromptFg:           "82",
		SeparatorFg:             "240",
		DirectoryBorderFg:       "63",
		DimFg:                   "250", // lighter so dim text (thinking, tool body) is readable
		ConfirmSeparatorFg:      "220",
		ConfirmToolFg:           "252",
		ConfirmHintFg:           "245",
		ConfirmAllowFg:          "82",
		ConfirmDenyFg:           "196",
		ConfirmEditFg:           "220",
		SidebarBorderFg:         "63",
		SidebarFocusedFg:        "230",
		SidebarFocusedBg:        "62",
		SidebarEntryFg:          "252",
		SidebarTaskFg:           "245",
		SidebarStatusFg:         "243",
		InfoPanelBg:             surfacePanel,
		InfoPanelSuccessFg:      "82",
		InfoPanelWarningFg:      "214",
		InfoPanelCriticalFg:     "196",
		InfoPanelPendingFg:      "240",
		InfoPanelDiagErrorFg:    "196",
		InfoPanelDiagWarnFg:     "214",
		InfoPanelDiagInfoFg:     "248",
		InfoPanelDiagHintFg:     "242",
		InfoPanelKeyWarnFg:      "214",
		InfoPanelKeyCriticalFg:  "196",
		InfoPanelRateWarnFg:     "214",
		InfoPanelRateCriticalFg: "196",
		AccentGradientFromFg:    "63",
		AccentGradientToFg:      "81",
		StatusPulseFromFg:       "63",
		StatusPulseToFg:         "205",
		RailUserFg:              "65",
		RailAssistantFg:         "61",
		RailToolFg:              "30",
		RailThinkingFg:          "243",
		RailErrorFg:             "196",
		RailUserFocusedFg:       "114",
		RailAssistantFocusedFg:  "111",
		RailToolFocusedFg:       "51",
		RailThinkingFocusedFg:   "252",
		RailErrorFocusedFg:      "203",
		ToastInfoBg:             "62",
		ToastInfoFg:             "230",
		ToastWarnBg:             "130",
		ToastWarnFg:             "230",
		ToastErrorBg:            "160",
		ToastErrorFg:            "230",
	}
}
