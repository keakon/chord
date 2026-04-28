package tui

// KeyMapFromConfig builds a KeyMap by starting from DefaultKeyMap() and
// overriding only the entries present in m. Keys are snake_case action names;
// values are slices of key strings (e.g. []string{"j", "down"}).
//
// Supported action names mirror the KeyMap fields in lower_snake_case:
//
//	insert_escape, insert_submit, insert_newline, insert_history_up, insert_history_down
//	insert_attach_clipboard, insert_attach_file, insert_clear_input
//	enter_insert, quit, help_toggle
//	scroll_down, scroll_up, full_page_down, full_page_up, scroll_to_bottom, scroll_to_top_seq
//	next_block, prev_block, toggle_collapse, fork_session, directory, usage_stats
//	search_start, search_next, search_prev
//	switch_agent, switch_role, switch_model
//
// Example config.yaml snippet:
//
//	keymap:
//	  next_block: ["j"]          # j moves to next message card
//	  prev_block: ["k"]          # k moves to previous message card
//	  scroll_down: ["down"]      # keep arrow key for line scrolling
//	  scroll_up: ["up"]          # keep arrow key for line scrolling
//	  quit: ["Q"]                # require shift for quit
func KeyMapFromConfig(m map[string][]string) KeyMap {
	km := DefaultKeyMap()
	if len(m) == 0 {
		return km
	}

	apply := func(field *[]string, key string) {
		if v, ok := m[key]; ok && len(v) > 0 {
			*field = v
		}
	}

	apply(&km.InsertEscape, "insert_escape")
	apply(&km.InsertSubmit, "insert_submit")
	apply(&km.InsertNewline, "insert_newline")
	apply(&km.InsertHistoryUp, "insert_history_up")
	apply(&km.InsertHistoryDown, "insert_history_down")
	apply(&km.InsertAttachClipboard, "insert_attach_clipboard")
	apply(&km.InsertAttachFile, "insert_attach_file")
	apply(&km.InsertClearInput, "insert_clear_input")
	apply(&km.EnterInsert, "enter_insert")
	apply(&km.Quit, "quit")
	apply(&km.HelpToggle, "help_toggle")
	apply(&km.ScrollDown, "scroll_down")
	apply(&km.ScrollUp, "scroll_up")
	apply(&km.FullPageDown, "full_page_down")
	apply(&km.FullPageUp, "full_page_up")
	apply(&km.ScrollToBottom, "scroll_to_bottom")
	apply(&km.ScrollToTopSeq, "scroll_to_top_seq")
	apply(&km.NextBlock, "next_block")
	apply(&km.PrevBlock, "prev_block")
	apply(&km.ToggleCollapse, "toggle_collapse")
	apply(&km.ForkSession, "fork_session")
	apply(&km.Directory, "directory")
	apply(&km.UsageStats, "usage_stats")
	apply(&km.SearchStart, "search_start")
	apply(&km.SearchNext, "search_next")
	apply(&km.SearchPrev, "search_prev")
	apply(&km.SwitchAgent, "switch_agent")
	apply(&km.SwitchRole, "switch_role")
	apply(&km.SwitchModel, "switch_model")

	return km
}
