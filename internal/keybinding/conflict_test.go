package keybinding

import (
	"reflect"
	"testing"
)

func TestConflictsClosedRules(t *testing.T) {
	bindings := Resolve(map[string][]string{
		string(ActionStop):       {"ctrl+c", "super+c", "alt+x", "ctrl+m", "ctrl+shift+c"},
		string(ActionInsertUndo): {"ctrl+c", "super+c", "alt+x", "ctrl+m", "ctrl+shift+c"},
		string(ActionSwitchRole): {"ctrl+c", "super+c", "alt+x", "ctrl+m", "ctrl+shift+c"},
	})
	got := Conflicts(bindings)
	want := []Conflict{
		{RuleGlobalCtrlC, ModeGlobal, "ctrl+c", WinnerCancelQuit, ActionInsertUndo},
		{RuleGlobalCtrlC, ModeGlobal, "ctrl+c", WinnerCancelQuit, ActionStop},
		{RuleGlobalCtrlC, ModeGlobal, "ctrl+c", WinnerCancelQuit, ActionSwitchRole},
		{RuleGlobalSuperC, ModeGlobal, "super+c", WinnerCopy, ActionInsertUndo},
		{RuleGlobalSuperC, ModeGlobal, "super+c", WinnerCopy, ActionStop},
		{RuleGlobalSuperC, ModeGlobal, "super+c", WinnerCopy, ActionSwitchRole},
		{RuleInsertStopUndo, ModeInsert, "alt+x", string(ActionStop), ActionInsertUndo},
		{RuleInsertStopUndo, ModeInsert, "ctrl+m", string(ActionStop), ActionInsertUndo},
		{RuleInsertStopUndo, ModeInsert, "ctrl+shift+c", string(ActionStop), ActionInsertUndo},
		{RuleNormalStopRole, ModeNormal, "alt+x", string(ActionStop), ActionSwitchRole},
		{RuleNormalStopRole, ModeNormal, "ctrl+shift+c", string(ActionStop), ActionSwitchRole},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("conflicts = %#v; want %#v", got, want)
	}
	if c := Conflicts(Resolve(map[string][]string{string(ActionQuit): {"ctrl+shift+c", "super+v"}, string(ActionEnterInsert): {"q"}})); len(c) != 0 {
		t.Fatalf("expanded closed rules: %v", c)
	}
	if c := Conflicts(Resolve(map[string][]string{string(ActionStop): {}})); len(c) != 0 {
		t.Fatalf("disabled action conflicts: %v", c)
	}
}
