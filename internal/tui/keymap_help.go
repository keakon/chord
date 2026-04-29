package tui

import "strings"

// HelpBinding describes one key binding in the generated help output.
type HelpBinding struct {
	Keys []string
	Help string
}

// HelpGroup groups help bindings by interaction mode or overlay.
type HelpGroup struct {
	Title    string
	Bindings []HelpBinding
}

func helpBinding(keys []string, help string) HelpBinding {
	return HelpBinding{Keys: keys, Help: help}
}

func keysDisplay(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return strings.Join(keys, " / ")
}

func keyBindingContains(keys []string, target string) bool {
	for _, key := range keys {
		if key == target {
			return true
		}
	}
	return false
}

// HelpGroups returns grouped help metadata derived from the active keymap.
func (km KeyMap) HelpGroups() []HelpGroup {
	normalBindings := []HelpBinding{
		helpBinding([]string{"[count]"}, "prefix repeat/absolute jump count (1-9999)"),
		helpBinding([]string{"esc"}, "clear pending chord or cancel focused/main turn"),
		helpBinding(km.EnterInsert, "enter insert mode"),
		helpBinding(km.HelpToggle, "open help"),
		helpBinding(km.SearchStart, "start search"),
		helpBinding(km.SearchNext, "next search match"),
		helpBinding(km.SearchPrev, "previous search match"),
		helpBinding(km.NextBlock, "next message card"),
		helpBinding(km.PrevBlock, "previous message card"),
		helpBinding([]string{"[count] " + keysDisplay(km.NextBlock)}, "repeat next message card"),
		helpBinding([]string{"[count] " + keysDisplay(km.PrevBlock)}, "repeat previous message card"),
		helpBinding(km.ScrollDown, "scroll down one line"),
		helpBinding(km.ScrollUp, "scroll up one line"),
		helpBinding([]string{"[count] " + keysDisplay(km.ScrollDown)}, "repeat line scroll down"),
		helpBinding([]string{"[count] " + keysDisplay(km.ScrollUp)}, "repeat line scroll up"),
		helpBinding(km.FullPageDown, "page down"),
		helpBinding(km.FullPageUp, "page up"),
		helpBinding([]string{"gg"}, "jump to first visible message card"),
		helpBinding([]string{"[count]gg / [count]G"}, "jump to visible message card by number"),
		helpBinding(km.ScrollToBottom, "jump to bottom"),
		helpBinding([]string{"yy"}, "copy current message card"),
		helpBinding([]string{"[count]yy"}, "copy count message cards from current card"),
		helpBinding([]string{"dd / [count]dd"}, "clear input and attachments"),
		helpBinding(km.ToggleCollapse, "toggle focused message card"),
		helpBinding([]string{"ee"}, "fork from user message"),
		helpBinding(km.Directory, "open message directory"),
		helpBinding(km.UsageStats, "open usage stats"),
		helpBinding(km.SwitchAgent, "switch focused agent"),
		helpBinding(km.SwitchRole, "switch main agent role"),
		helpBinding(km.SwitchModel, "open model selector"),
		helpBinding(km.Diagnostics, "export diagnostics bundle"),
		helpBinding(km.Quit, "quit"),
	}
	if keyBindingContains(km.ToggleCollapse, "enter") {
		normalBindings = append(normalBindings, helpBinding([]string{"enter"}, "open linked delegate worker"))
	}

	return []HelpGroup{
		{
			Title: "Insert Mode",
			Bindings: []HelpBinding{
				helpBinding(km.InsertEscape, "exit insert mode"),
				helpBinding(km.InsertSubmit, "send or continue"),
				helpBinding(km.InsertNewline, "insert newline"),
				helpBinding(km.InsertHistoryUp, "previous history"),
				helpBinding(km.InsertHistoryDown, "next history"),
				helpBinding(km.InsertAttachClipboard, "paste image or text"),
				helpBinding(km.InsertAttachFile, "attach image file"),
				helpBinding(km.InsertClearInput, "clear input"),
				helpBinding(km.SwitchRole, "switch main agent role"),
				helpBinding(km.SwitchAgent, "switch focused agent"),
				helpBinding(km.SwitchModel, "open model selector"),
				helpBinding(km.Diagnostics, "export diagnostics bundle"),
			},
		},
		{
			Title:    "Normal Mode",
			Bindings: normalBindings,
		},
		{
			Title: "Search",
			Bindings: []HelpBinding{
				helpBinding([]string{"enter"}, "run search"),
				helpBinding(km.SearchNext, "next match"),
				helpBinding(km.SearchPrev, "previous match"),
				helpBinding([]string{"esc"}, "cancel search"),
			},
		},
		{
			Title: "Confirm",
			Bindings: []HelpBinding{
				helpBinding([]string{"y"}, "allow"),
				helpBinding([]string{"n"}, "deny"),
				helpBinding([]string{"e"}, "edit arguments"),
				helpBinding([]string{"tab"}, "toggle details"),
				helpBinding([]string{"esc"}, "cancel"),
			},
		},
		{
			Title: "Question",
			Bindings: []HelpBinding{
				helpBinding([]string{"j / down"}, "move down"),
				helpBinding([]string{"k / up"}, "move up"),
				helpBinding([]string{"space"}, "toggle option"),
				helpBinding([]string{"tab"}, "toggle custom input"),
				helpBinding([]string{"shift+enter / ctrl+j"}, "insert newline"),
				helpBinding([]string{"enter"}, "confirm"),
				helpBinding([]string{"esc"}, "cancel"),
			},
		},
		{
			Title: "Message Directory",
			Bindings: []HelpBinding{
				helpBinding([]string{"j / down"}, "move down"),
				helpBinding([]string{"k / up"}, "move up"),
				helpBinding([]string{"g"}, "jump to top"),
				helpBinding([]string{"G"}, "jump to bottom"),
				helpBinding([]string{"enter"}, "jump to message"),
				helpBinding([]string{"esc / q"}, "close"),
			},
		},
		{
			Title: "Model Selector",
			Bindings: []HelpBinding{
				helpBinding([]string{"type text"}, "filter provider/model"),
				helpBinding([]string{"backspace"}, "delete filter"),
				helpBinding([]string{"j / down"}, "move down"),
				helpBinding([]string{"k / up"}, "move up"),
				helpBinding([]string{"g"}, "jump to top"),
				helpBinding([]string{"G"}, "jump to bottom"),
				helpBinding([]string{"enter"}, "switch model"),
				helpBinding(append([]string{}, km.SwitchModel...), "close"),
			},
		},
		{
			Title: "Usage Stats",
			Bindings: []HelpBinding{
				helpBinding([]string{"tab / shift+tab"}, "switch view"),
				helpBinding([]string{"s"}, "toggle scope"),
				helpBinding([]string{"r"}, "cycle range / retry"),
				helpBinding([]string{"j / down"}, "scroll down"),
				helpBinding([]string{"k / up"}, "scroll up"),
				helpBinding([]string{"ctrl+f"}, "page down"),
				helpBinding([]string{"ctrl+b"}, "page up"),
				helpBinding([]string{"g"}, "jump to top"),
				helpBinding([]string{"G"}, "jump to bottom"),
				helpBinding(append([]string{}, km.UsageStats...), "close"),
				helpBinding([]string{"esc / q"}, "close"),
			},
		},
		{
			Title: "Session Selector",
			Bindings: []HelpBinding{
				helpBinding([]string{"j / down"}, "move down"),
				helpBinding([]string{"k / up"}, "move up"),
				helpBinding([]string{"g"}, "jump to top"),
				helpBinding([]string{"G"}, "jump to bottom"),
				helpBinding([]string{"enter"}, "resume session"),
				helpBinding([]string{"d"}, "delete selected session"),
				helpBinding([]string{"esc"}, "close"),
			},
		},
		{
			Title: "Handoff Selector",
			Bindings: []HelpBinding{
				helpBinding([]string{"j / down"}, "move down"),
				helpBinding([]string{"k / up"}, "move up"),
				helpBinding([]string{"g"}, "jump to top"),
				helpBinding([]string{"G"}, "jump to bottom"),
				helpBinding([]string{"enter"}, "confirm handoff"),
				helpBinding([]string{"esc"}, "close"),
			},
		},
		{
			Title: "Help",
			Bindings: []HelpBinding{
				helpBinding([]string{"j / down"}, "scroll down"),
				helpBinding([]string{"k / up"}, "scroll up"),
				helpBinding([]string{"ctrl+f"}, "page down"),
				helpBinding([]string{"ctrl+b"}, "page up"),
				helpBinding([]string{"g"}, "jump to top"),
				helpBinding([]string{"G"}, "jump to bottom"),
				helpBinding(append([]string{}, km.HelpToggle...), "close"),
				helpBinding([]string{"esc / q"}, "close"),
			},
		},
		{
			Title: "Image Viewer",
			Bindings: []HelpBinding{
				helpBinding([]string{"← / h"}, "previous image"),
				helpBinding([]string{"→ / l"}, "next image"),
				helpBinding([]string{"esc / q"}, "close viewer"),
			},
		},
	}
}
