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
	InsertAttachClipboard []string // smart paste: image first, then text
	InsertAttachFile      []string // pick image from file
	InsertClearInput      []string // clear input box and attachments

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
	NextBlock      []string
	PrevBlock      []string
	ToggleCollapse []string
	ForkSession    []string

	// Normal mode – overlays
	Directory  []string
	UsageStats []string

	// Normal mode – search
	SearchStart []string // enter search mode
	SearchNext  []string // jump to next match
	SearchPrev  []string // jump to previous match

	// Normal and Insert modes – multi-agent
	SwitchAgent []string // cycle focused agent view (Shift+Tab)
	SwitchRole  []string // cycle main agent role (Tab, only in main agent view)

	// Both Insert and Normal modes
	SwitchModel []string // open model selector
	Diagnostics []string // export diagnostics bundle
}

// DefaultKeyMap returns the built-in Vim-style key bindings.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		// Insert mode
		InsertEscape:          []string{"esc"},
		InsertSubmit:          []string{"enter"},
		InsertNewline:         []string{"shift+enter", "ctrl+j"},
		InsertHistoryUp:       []string{"up", "ctrl+p"},
		InsertHistoryDown:     []string{"down", "ctrl+n"},
		InsertAttachClipboard: []string{"ctrl+v"},
		InsertAttachFile:      []string{"ctrl+f"},
		InsertClearInput:      []string{"ctrl+u"},

		// Normal mode – mode switches
		EnterInsert: []string{"i"},
		Quit:        []string{"q"},
		HelpToggle:  []string{"?"},

		// Normal mode – scrolling
		ScrollDown:     []string{"down"},
		ScrollUp:       []string{"up"},
		FullPageDown:   []string{"ctrl+f"},
		FullPageUp:     []string{"ctrl+b"},
		ScrollToBottom: []string{"G"},
		ScrollToTopSeq: []string{"g"},

		// Normal mode – block navigation
		NextBlock:      []string{"j", "}"},
		PrevBlock:      []string{"k", "{"},
		ToggleCollapse: []string{"o", "enter", " ", "space"},
		ForkSession:    []string{"e"},

		// Normal mode – overlays
		Directory:  []string{"ctrl+j"},
		UsageStats: []string{"$"},

		// Normal mode – search
		SearchStart: []string{"/"},
		SearchNext:  []string{"n"},
		SearchPrev:  []string{"N"},

		// Normal mode – multi-agent
		SwitchAgent: []string{"shift+tab"},
		SwitchRole:  []string{"tab"},

		// Both Insert and Normal modes
		SwitchModel: []string{"ctrl+p"},
		Diagnostics: []string{"ctrl+g"},
	}
}

// keyMatches returns true if key matches any of the given bindings.
func keyMatches(key string, bindings []string) bool {
	return slices.Contains(bindings, key)
}
