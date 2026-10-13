package keybinding

import (
	"cmp"
	"slices"
)

const (
	RuleGlobalCtrlC    = "global_ctrl_c"
	RuleGlobalSuperC   = "global_super_c"
	RuleInsertStopUndo = "insert_stop_undo"
	RuleNormalStopRole = "normal_stop_role"
	ModeGlobal         = "global"
	ModeInsert         = "insert"
	ModeNormal         = "normal"
	WinnerCancelQuit   = "cancel_or_quit"
	WinnerCopy         = "copy"
)

type Conflict struct {
	RuleID string
	Mode   string
	Key    string
	Winner string
	Loser  ActionID
}

// Conflicts reports only the four proven handler precedence rules. It neither
// changes bindings nor claims to model overlays or every possible collision.
func Conflicts(bindings map[ActionID][]string) []Conflict {
	var result []Conflict
	for _, action := range actions {
		for _, key := range bindings[action.ID] {
			switch key {
			case "ctrl+c":
				result = append(result, Conflict{RuleGlobalCtrlC, ModeGlobal, key, WinnerCancelQuit, action.ID})
			case "super+c":
				result = append(result, Conflict{RuleGlobalSuperC, ModeGlobal, key, WinnerCopy, action.ID})
			}
		}
	}
	for _, key := range bindings[ActionStop] {
		if key == "ctrl+c" || key == "super+c" {
			continue
		}
		if slices.Contains(bindings[ActionInsertUndo], key) {
			result = append(result, Conflict{RuleInsertStopUndo, ModeInsert, key, string(ActionStop), ActionInsertUndo})
		}
		if key != "ctrl+m" && slices.Contains(bindings[ActionSwitchRole], key) {
			result = append(result, Conflict{RuleNormalStopRole, ModeNormal, key, string(ActionStop), ActionSwitchRole})
		}
	}
	slices.SortFunc(result, func(a, b Conflict) int {
		if n := cmp.Compare(a.RuleID, b.RuleID); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Loser, b.Loser); n != 0 {
			return n
		}
		return cmp.Compare(a.Key, b.Key)
	})
	return result
}
