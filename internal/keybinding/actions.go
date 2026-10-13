// Package keybinding defines configurable actions and terminal key syntax.
package keybinding

import "slices"

type ActionID string

type BindingKind uint8

const (
	BindingSingle BindingKind = iota
	BindingRepeatPrefix
)

type Action struct {
	ID      ActionID
	Kind    BindingKind
	Default []string
}

const (
	ActionInsertEscape          ActionID = "insert_escape"
	ActionInsertSubmit          ActionID = "insert_submit"
	ActionInsertNewline         ActionID = "insert_newline"
	ActionInsertHistorySearch   ActionID = "insert_history_search"
	ActionInsertHistoryUp       ActionID = "insert_history_up"
	ActionInsertHistoryDown     ActionID = "insert_history_down"
	ActionInsertAttachClipboard ActionID = "insert_attach_clipboard"
	ActionInsertAttachFile      ActionID = "insert_attach_file"
	ActionInsertClearInput      ActionID = "insert_clear_input"
	ActionInsertUndo            ActionID = "insert_undo"
	ActionInsertPageUp          ActionID = "insert_page_up"
	ActionInsertPageDown        ActionID = "insert_page_down"
	ActionEnterInsert           ActionID = "enter_insert"
	ActionQuit                  ActionID = "quit"
	ActionHelpToggle            ActionID = "help_toggle"
	ActionScrollDown            ActionID = "scroll_down"
	ActionScrollUp              ActionID = "scroll_up"
	ActionFullPageDown          ActionID = "full_page_down"
	ActionFullPageUp            ActionID = "full_page_up"
	ActionScrollToBottom        ActionID = "scroll_to_bottom"
	ActionScrollToTopSeq        ActionID = "scroll_to_top_seq"
	ActionNextBlock             ActionID = "next_block"
	ActionPrevBlock             ActionID = "prev_block"
	ActionNextUserBlock         ActionID = "next_user_block"
	ActionPrevUserBlock         ActionID = "prev_user_block"
	ActionNextAssistantBlock    ActionID = "next_assistant_block"
	ActionPrevAssistantBlock    ActionID = "prev_assistant_block"
	ActionNextSameTypeBlock     ActionID = "next_same_type_block"
	ActionPrevSameTypeBlock     ActionID = "prev_same_type_block"
	ActionToggleCollapse        ActionID = "toggle_collapse"
	ActionForkSession           ActionID = "fork_session"
	ActionDirectory             ActionID = "directory"
	ActionUsageStats            ActionID = "usage_stats"
	ActionErrorPanel            ActionID = "error_panel"
	ActionBackgroundJobs        ActionID = "background_jobs"
	ActionSearchStart           ActionID = "search_start"
	ActionSearchNext            ActionID = "search_next"
	ActionSearchPrev            ActionID = "search_prev"
	ActionSwitchAgent           ActionID = "switch_agent"
	ActionSwitchRole            ActionID = "switch_role"
	ActionStop                  ActionID = "stop"
	ActionSwitchModel           ActionID = "switch_model"
	ActionServiceTier           ActionID = "service_tier"
	ActionDiagnostics           ActionID = "diagnostics"
	ActionYolo                  ActionID = "yolo"
	ActionMCP                   ActionID = "mcp"
)

var actions = []Action{
	{ActionInsertEscape, BindingSingle, []string{"esc"}},
	{ActionInsertSubmit, BindingSingle, []string{"enter"}},
	{ActionInsertNewline, BindingSingle, []string{"shift+enter", "ctrl+j"}},
	{ActionInsertHistorySearch, BindingSingle, []string{"ctrl+r"}},
	{ActionInsertHistoryUp, BindingSingle, []string{"up"}},
	{ActionInsertHistoryDown, BindingSingle, []string{"down", "ctrl+n"}},
	{ActionInsertAttachClipboard, BindingSingle, []string{"ctrl+v", "alt+v"}},
	{ActionInsertAttachFile, BindingSingle, []string{}},
	{ActionInsertClearInput, BindingSingle, []string{"ctrl+u"}},
	{ActionInsertUndo, BindingSingle, []string{"ctrl+z"}},
	{ActionInsertPageUp, BindingSingle, []string{"pgup"}},
	{ActionInsertPageDown, BindingSingle, []string{"pgdown"}},
	{ActionEnterInsert, BindingSingle, []string{"i"}},
	{ActionQuit, BindingSingle, []string{"q"}},
	{ActionHelpToggle, BindingSingle, []string{"?"}},
	{ActionScrollDown, BindingSingle, []string{"down"}},
	{ActionScrollUp, BindingSingle, []string{"up"}},
	{ActionFullPageDown, BindingSingle, []string{"ctrl+f", "pgdown"}},
	{ActionFullPageUp, BindingSingle, []string{"ctrl+b", "pgup"}},
	{ActionScrollToBottom, BindingSingle, []string{"G"}},
	{ActionScrollToTopSeq, BindingRepeatPrefix, []string{"g"}},
	{ActionNextBlock, BindingSingle, []string{"j"}},
	{ActionPrevBlock, BindingSingle, []string{"k"}},
	{ActionNextUserBlock, BindingSingle, []string{"}"}},
	{ActionPrevUserBlock, BindingSingle, []string{"{"}},
	{ActionNextAssistantBlock, BindingSingle, []string{")"}},
	{ActionPrevAssistantBlock, BindingSingle, []string{"("}},
	{ActionNextSameTypeBlock, BindingSingle, []string{"]"}},
	{ActionPrevSameTypeBlock, BindingSingle, []string{"["}},
	{ActionToggleCollapse, BindingSingle, []string{"o", "enter", "space"}},
	{ActionForkSession, BindingRepeatPrefix, []string{"e"}},
	{ActionDirectory, BindingSingle, []string{"ctrl+t"}},
	{ActionUsageStats, BindingSingle, []string{"$"}},
	{ActionErrorPanel, BindingSingle, []string{"ctrl+e"}},
	{ActionBackgroundJobs, BindingSingle, []string{"ctrl+j"}},
	{ActionSearchStart, BindingSingle, []string{"/"}},
	{ActionSearchNext, BindingSingle, []string{"n"}},
	{ActionSearchPrev, BindingSingle, []string{"N"}},
	{ActionSwitchAgent, BindingSingle, []string{"shift+tab"}},
	{ActionSwitchRole, BindingSingle, []string{"alt+r"}},
	{ActionStop, BindingSingle, []string{"ctrl+x"}},
	{ActionSwitchModel, BindingSingle, []string{"ctrl+p"}},
	{ActionServiceTier, BindingSingle, []string{"ctrl+r"}},
	{ActionDiagnostics, BindingSingle, []string{"ctrl+g"}},
	{ActionYolo, BindingSingle, []string{"ctrl+y"}},
	{ActionMCP, BindingSingle, []string{"ctrl+o"}},
}

// Actions returns the complete ordered catalog with independent default slices.
func Actions() []Action {
	result := slices.Clone(actions)
	for i := range result {
		result[i].Default = slices.Clone(result[i].Default)
	}
	return result
}

func Lookup(id ActionID) (Action, bool) {
	for _, action := range actions {
		if action.ID == id {
			action.Default = slices.Clone(action.Default)
			return action, true
		}
	}
	return Action{}, false
}

// Resolve returns all effective bindings. An explicitly present empty or nil
// slice disables an action. Unknown actions and invalid lists are ignored.
func Resolve(overrides map[string][]string) map[ActionID][]string {
	result := make(map[ActionID][]string, len(actions))
	for _, action := range Actions() {
		result[action.ID] = action.Default
		if keys, exists := overrides[string(action.ID)]; exists {
			normalized := make([]string, 0, len(keys))
			valid := true
			for _, raw := range keys {
				key, err := NormalizeKey(raw)
				if err != nil {
					valid = false
					break
				}
				if !slices.Contains(normalized, key) {
					normalized = append(normalized, key)
				}
			}
			if valid {
				result[action.ID] = normalized
			}
		}
	}
	return result
}
