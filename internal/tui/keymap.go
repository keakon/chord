package tui

import "slices"

// KeyMap defines all keyboard bindings for the TUI. Each field is a slice of
// key strings (as returned by tea.KeyMsg.String()) so that multiple keys can
// be bound to the same action.
type KeyMap struct {
	// Insert mode
	InsertEscape          []string
	InsertSubmit          []string
	InsertNewline         []string
	InsertHistoryUp       []string
	InsertHistoryDown     []string
	InsertAttachClipboard []string // attach image/PDF from the system clipboard
	InsertAttachFile      []string // pick image from file (optional; default unbound)
	InsertClearInput      []string // clear input box and attachments
	InsertPageUp          []string // page the transcript up without leaving insert mode
	InsertPageDown        []string // page the transcript down without leaving insert mode

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
	Directory  []string
	UsageStats []string
	ErrorPanel []string

	// Normal mode – search
	SearchStart []string // enter search mode
	SearchNext  []string // jump to next match
	SearchPrev  []string // jump to previous match

	// Multi-agent switching. Both default to Shift+Tab and are told apart by
	// the input mode: Insert cycles the role (a role change is normally
	// followed by typing), Normal cycles the focused view (a view change is
	// normally followed by scrolling). Only the action belonging to the
	// current mode is consulted, so the shared default is not a conflict.
	SwitchAgent []string // cycle focused agent view (Shift+Tab, Normal mode)
	SwitchRole  []string // cycle main agent role (Shift+Tab, Insert mode, only when focused on main)

	// Both Insert and Normal modes
	SwitchModel []string // open model pool selector
	ServiceTier []string // switch service tier for subsequent model requests
	Diagnostics []string // export diagnostics bundle
	Yolo        []string // toggle YOLO permission-bypass mode
	MCP         []string // open MCP server selector
}

// DefaultKeyMap returns the built-in Vim-style key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		// Insert mode
		InsertEscape:          []string{"esc"},
		InsertSubmit:          []string{"enter"},
		InsertNewline:         []string{"shift+enter", "ctrl+j"},
		InsertHistoryUp:       []string{"up"},
		InsertHistoryDown:     []string{"down", "ctrl+n"},
		InsertAttachClipboard: []string{"ctrl+v", "alt+v"},
		InsertAttachFile:      nil,
		InsertClearInput:      []string{"ctrl+u"},
		// PgUp/PgDown scroll the transcript instead of the textarea cursor: the
		// input box is height-clamped to a few lines, so in-input paging is
		// nearly useless while glancing back at earlier output is common.
		InsertPageUp:   []string{"pgup"},
		InsertPageDown: []string{"pgdown"},

		// Normal mode – mode switches
		EnterInsert: []string{"i"},
		Quit:        []string{"q"},
		HelpToggle:  []string{"?"},

		// Normal mode – scrolling
		ScrollDown:     []string{"down"},
		ScrollUp:       []string{"up"},
		FullPageDown:   []string{"ctrl+f", "pgdown"},
		FullPageUp:     []string{"ctrl+b", "pgup"},
		ScrollToBottom: []string{"G"},
		ScrollToTopSeq: []string{"g"},

		// Normal mode – block navigation
		NextBlock:          []string{"j"},
		PrevBlock:          []string{"k"},
		NextUserBlock:      []string{"}"},
		PrevUserBlock:      []string{"{"},
		NextAssistantBlock: []string{")"},
		PrevAssistantBlock: []string{"("},
		NextSameTypeBlock:  []string{"]"},
		PrevSameTypeBlock:  []string{"["},
		ToggleCollapse:     []string{"o", "enter", " ", "space"},
		ForkSession:        []string{"e"},

		// Normal mode – overlays
		Directory:  []string{"ctrl+t"},
		UsageStats: []string{"$"},
		ErrorPanel: []string{"ctrl+e"},

		// Normal mode – search
		SearchStart: []string{"/"},
		SearchNext:  []string{"n"},
		SearchPrev:  []string{"N"},

		// Normal mode – multi-agent
		SwitchAgent: []string{"shift+tab"},
		SwitchRole:  []string{"shift+tab"},

		// Both Insert and Normal modes
		SwitchModel: []string{"ctrl+p"},
		ServiceTier: []string{"ctrl+r"},
		Diagnostics: []string{"ctrl+g"},
		Yolo:        []string{"ctrl+y"},
		MCP:         []string{"ctrl+o"},
	}
}

// keyMatches returns true if key matches any of the given bindings.
func keyMatches(key string, bindings []string) bool {
	return slices.Contains(bindings, key)
}

func (m *Model) overlayCloseKeyMatches(key string, toggleBindings []string) bool {
	return key == "esc" || key == "q" || keyMatches(key, m.keyMap.Quit) || keyMatches(key, toggleBindings)
}
