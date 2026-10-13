package tui

import "strings"

// welcomeKeyHints describes only shortcuts available in the active mode.
func welcomeKeyHints(mode Mode, km KeyMap) []string {
	var lines []string
	addLine := func(parts ...string) {
		var visible []string
		for _, part := range parts {
			if part != "" {
				visible = append(visible, part)
			}
		}
		if len(visible) > 0 {
			lines = append(lines, strings.Join(visible, "  "))
		}
	}
	binding := func(keys []string, description string) string {
		if len(keys) == 0 {
			return ""
		}
		return keysDisplay(keys) + ": " + description
	}
	if mode == ModeInsert {
		if len(km.InsertSubmit) > 0 {
			addLine("Type your task and press " + keysDisplay(km.InsertSubmit) + " to send · /help for commands")
		} else {
			addLine("Send shortcut is unbound; configure keymap.insert_submit to send.")
		}
		normal := binding(km.InsertEscape, "normal mode")
		if normal != "" && len(km.HelpToggle) > 0 {
			normal += " (" + keysDisplay(km.HelpToggle) + " for help)"
		}
		addLine(normal, binding(km.SwitchModel, "model pool"))
		addLine(binding(km.InsertAttachClipboard, "attach image/PDF"), binding(km.InsertAttachFile, "attach file"), "terminal paste: text")
	} else {
		var help []string
		if len(km.HelpToggle) > 0 {
			help = append(help, "Press "+keysDisplay(km.HelpToggle)+" for help")
		}
		if len(km.InsertSubmit) > 0 && len(km.EnterInsert) > 0 {
			help = append(help, "/help for commands")
		}
		addLine(strings.Join(help, " · "))
		addLine(binding(km.EnterInsert, "insert"), binding(km.SearchStart, "search"), binding(km.SwitchModel, "model pool"), binding(km.Directory, "directory"))
	}
	return lines
}
