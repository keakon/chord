package tui

import (
	"reflect"
	"slices"
	"testing"

	"github.com/keakon/chord/internal/keybinding"
)

func TestKeyMapSetterCoverage(t *testing.T) {
	fields := reflect.TypeFor[KeyMap]().NumField()
	if len(keyMapSetters) != fields || len(keyMapSetters) != len(keybinding.Actions()) {
		t.Fatalf("setters/catalog/fields differ")
	}
	seenIDs := map[keybinding.ActionID]bool{}
	seenFields := map[int]bool{}
	for _, setter := range keyMapSetters {
		if _, ok := keybinding.Lookup(setter.id); !ok || seenIDs[setter.id] {
			t.Fatalf("invalid/duplicate setter %s", setter.id)
		}
		seenIDs[setter.id] = true
		km := KeyMap{}
		*setter.field(&km) = []string{"sentinel"}
		changed := 0
		value := reflect.ValueOf(km)
		for i := range fields {
			if value.Field(i).Len() > 0 {
				changed++
				if seenFields[i] {
					t.Fatalf("field %s set twice", value.Type().Field(i).Name)
				}
				seenFields[i] = true
			}
		}
		if changed != 1 {
			t.Fatalf("setter %s changes %d fields", setter.id, changed)
		}
	}
}

func TestKeyMapFromConfigResolvedCopies(t *testing.T) {
	input := map[string][]string{string(keybinding.ActionQuit): nil, string(keybinding.ActionInsertUndo): {}, string(keybinding.ActionStop): {"alt+ctrl+x", "ctrl+alt+x"}, string(keybinding.ActionEnterInsert): {"bad key"}}
	km := KeyMapFromConfig(input)
	if km.Quit == nil || len(km.Quit) != 0 || km.InsertUndo == nil || len(km.InsertUndo) != 0 {
		t.Fatal("empty bindings did not disable")
	}
	if !slices.Equal(km.Stop, []string{"ctrl+alt+x"}) || !slices.Equal(km.EnterInsert, []string{"i"}) {
		t.Fatal("resolution was bypassed")
	}
	input[string(keybinding.ActionStop)][0] = "q"
	if km.Stop[0] != "ctrl+alt+x" {
		t.Fatal("binding aliases input")
	}
	km.EnterInsert[0] = "x"
	if DefaultKeyMap().EnterInsert[0] != "i" {
		t.Fatal("defaults alias mutable result")
	}
	for _, setter := range keyMapSetters {
		input[string(setter.id)] = nil
	}
	allDisabled := KeyMapFromConfig(input)
	for _, setter := range keyMapSetters {
		if len(*setter.field(&allDisabled)) != 0 {
			t.Errorf("%s retained defaults", setter.id)
		}
	}
}
