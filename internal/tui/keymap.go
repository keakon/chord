package tui

import "slices"

// KeyMap defines all keyboard bindings for the TUI. Each field is a slice of
// key strings (as returned by tea.KeyMsg.String()) so that multiple keys can
// be bound to the same action.
type KeyMap struct {
	// Insert mode
	InsertEscape                []string
	InsertSubmit                []string
	InsertNewline               []string
	InsertHistorySearch         []string
	InsertHistoryUp             []string
	InsertHistoryDown           []string
	InsertAttachClipboard       []string // attach image/PDF from the system clipboard
	InsertAttachFile            []string // pick image from file (optional; default unbound)
	InsertClearInput            []string // clear input box and attachments
	InsertUndo                  []string // undo the last composer edit
	InsertPageUp                []string // page the transcript up without leaving insert mode
	InsertPageDown              []string // page the transcript down without leaving insert mode
	InsertSubmitAfterCompletion []string

	// Normal mode – mode switches
	EnterInsert []string
	Quit        []string
	HelpToggle  []string

	// Normal mode – scrolling
	ScrollDown     []string
	ScrollUp       []string
	FullPageDown   []string
	FullPageUp     []string
	ScrollToBottom []string
	ScrollToTopSeq []string // first key of the two-key "gg" sequence

	// Normal mode – block navigation
	NextBlock          []string
	PrevBlock          []string
	NextUserBlock      []string // }  next user card (turn boundary)
	PrevUserBlock      []string // {  previous user card (turn boundary)
	NextAssistantBlock []string // )  next assistant card
	PrevAssistantBlock []string // (  previous assistant card
	NextSameTypeBlock  []string // ]  next card of the focused card's type
	PrevSameTypeBlock  []string // [  previous card of the focused card's type
	ToggleCollapse     []string
	ForkSession        []string

	// Normal mode – overlays
	Directory      []string
	UsageStats     []string
	ErrorPanel     []string
	BackgroundJobs []string // list running background jobs (also the stop entry point)

	// Normal mode – search
	SearchStart []string // enter search mode
	SearchNext  []string // jump to next match
	SearchPrev  []string // jump to previous match

	// Agent view and role actions have the same meaning in Insert and Normal modes.
	SwitchAgent []string // cycle the focused agent view
	SwitchRole  []string // cycle the main agent role while viewing main
	Stop        []string // stop the current operation without closing the session

	// Both Insert and Normal modes
	SwitchModel []string // open model pool selector
	ServiceTier []string // switch service tier for subsequent model requests
	Diagnostics []string // export diagnostics bundle
	Yolo        []string // toggle YOLO permission-bypass mode
	MCP         []string // open MCP server selector
}

// DefaultKeyMap returns the built-in Vim-style key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMapFromConfig(nil)
}

// keyMatches returns true if key matches any of the given bindings.
func keyMatches(key string, bindings []string) bool {
	return slices.Contains(bindings, key)
}

func (m *Model) overlayCloseKeyMatches(key string, toggleBindings []string) bool {
	return key == "esc" || key == "q" || keyMatches(key, m.keyMap.Quit) || keyMatches(key, toggleBindings)
}
