package keybinding

import (
	"reflect"
	"slices"
	"testing"
)

func TestActionsCatalog(t *testing.T) {
	catalog := Actions()
	if len(catalog) != 47 {
		t.Fatalf("catalog has %d actions", len(catalog))
	}
	seen := map[ActionID]bool{}
	for _, action := range catalog {
		if seen[action.ID] {
			t.Errorf("duplicate %s", action.ID)
		}
		seen[action.ID] = true
		if action.Default == nil {
			t.Errorf("nil default for %s", action.ID)
		}
		for _, key := range action.Default {
			if normalized, err := NormalizeKey(key); err != nil || normalized != key {
				t.Errorf("%s default %q: %q, %v", action.ID, key, normalized, err)
			}
		}
		repeat := action.ID == ActionScrollToTopSeq || action.ID == ActionForkSession
		if (action.Kind == BindingRepeatPrefix) != repeat {
			t.Errorf("kind for %s = %v", action.ID, action.Kind)
		}
		found, ok := Lookup(action.ID)
		if !ok || !reflect.DeepEqual(found, action) {
			t.Errorf("lookup %s differs", action.ID)
		}
	}
	if _, ok := Lookup("unknown"); ok {
		t.Fatal("unknown action found")
	}
	if conflicts := Conflicts(Resolve(nil)); len(conflicts) != 0 {
		t.Fatalf("default conflicts: %+v", conflicts)
	}
}

func TestResolveOwnershipAndValidation(t *testing.T) {
	overrides := map[string][]string{
		string(ActionQuit):        {"Q", "Q", " ", "space", "g", "G", "ctrl+i", "tab", "ctrl+m", "enter"},
		string(ActionInsertUndo):  nil,
		string(ActionStop):        {},
		string(ActionEnterInsert): {"a", "invalid"},
		"unknown":                 {"x"},
	}
	got := Resolve(overrides)
	want := []string{"Q", "space", "g", "G", "ctrl+i", "tab", "ctrl+m", "enter"}
	if !slices.Equal(got[ActionQuit], want) {
		t.Fatalf("quit = %v", got[ActionQuit])
	}
	for _, id := range []ActionID{ActionInsertUndo, ActionStop, ActionInsertAttachFile} {
		if got[id] == nil || len(got[id]) != 0 {
			t.Errorf("%s not non-nil disabled", id)
		}
	}
	if !slices.Equal(got[ActionEnterInsert], []string{"i"}) {
		t.Fatal("invalid action partially accepted")
	}
	if _, ok := got["unknown"]; ok {
		t.Fatal("unknown action included")
	}
	overrides[string(ActionQuit)][0] = "x"
	if got[ActionQuit][0] != "Q" {
		t.Fatal("input aliases output")
	}
	catalog := Actions()
	catalog[0].Default[0] = "x"
	catalog[0].ID = "unknown"
	lookup, _ := Lookup(ActionInsertEscape)
	lookup.Default[0] = "y"
	resolved := Resolve(nil)
	resolved[ActionInsertEscape][0] = "z"
	if Actions()[0].ID != ActionInsertEscape || Actions()[0].Default[0] != "esc" || Resolve(nil)[ActionInsertEscape][0] != "esc" {
		t.Fatal("catalog storage exposed")
	}
}
