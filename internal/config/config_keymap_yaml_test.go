package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/keybinding"
)

func TestKeymapActionAliasesAcrossLoaders(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
		line       int
	}{
		{"action name", "model_templates:\n  action: &stop quit\nkeymap:\n  *stop: []\n", []string{}, 4},
		{"unknown anchor name", "model_templates:\n  action: &action quit\nkeymap:\n  *action: [Q]\n", []string{"Q"}, 4},
		{"explicit alias beats merge", "model_templates:\n  action: &stop quit\n  defaults: &defaults {quit: [q]}\nkeymap:\n  <<: *defaults\n  *stop: []\n", []string{}, 6},
		{"merged alias", "model_templates:\n  action: &stop quit\n  defaults: &defaults\n    *stop: []\nkeymap:\n  <<: *defaults\n", []string{}, 4},
		{"explicit beats merged alias", "model_templates:\n  action: &stop quit\n  defaults: &defaults\n    *stop: []\nkeymap:\n  quit: [Q]\n  <<: *defaults\n", []string{"Q"}, 6},
		{"alias then explicit", "model_templates:\n  action: &stop quit\nkeymap:\n  *stop: []\n  quit: [Q]\n", []string{"Q"}, 5},
		{"explicit then alias", "model_templates:\n  action: &stop quit\nkeymap:\n  quit: [Q]\n  *stop: []\n", []string{}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global, project := filepath.Join(t.TempDir(), "global.yaml"), filepath.Join(t.TempDir(), "project.yaml")
			for _, projectLayer := range []bool{false, true} {
				writeKeymapFile(t, global, tc.body)
				projectPath, source := "", global
				if projectLayer {
					writeKeymapFile(t, global, "keymap: {quit: [q]}\n")
					writeKeymapFile(t, project, tc.body)
					projectPath, source = project, project
				}
				rc, err := LoadResolvedConfig(global, projectPath)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(rc.Config.KeyMap["quit"], tc.want) {
					t.Fatalf("project=%v keys=%#v", projectLayer, rc.Config.KeyMap)
				}
				if _, ok := rc.Config.KeyMap["stop"]; ok {
					t.Fatal("anchor name was used as an action")
				}
				if ds := keymapTestDiagnostics(rc.Diagnostics); len(ds) != 0 {
					t.Fatal(ds)
				}
				origin, ok := keymapEffectiveOrigin(rc.Index, keybinding.ActionQuit)
				if !ok || origin.File != source || origin.Line != tc.line {
					t.Fatalf("origin=%+v", origin)
				}
				cfg, err := LoadConfigFromPath(global)
				if err != nil {
					t.Fatal(err)
				}
				if projectLayer {
					_, cfg, err = MergeProjectConfig(cfg, project)
					if err != nil {
						t.Fatal(err)
					}
				}
				if !reflect.DeepEqual(cfg.KeyMap, rc.Config.KeyMap) {
					t.Fatalf("API mismatch: %#v / %#v", cfg.KeyMap, rc.Config.KeyMap)
				}
			}
		})
	}
}

func TestKeymapInvalidContainersRetainValidSiblings(t *testing.T) {
	for _, bad := range []string{"{[a, b]: c}", "[Q, {[a, b]: c}]"} {
		for _, inherited := range []string{"[Q]", "[]"} {
			t.Run(bad+inherited, func(t *testing.T) {
				global, project := filepath.Join(t.TempDir(), "global.yaml"), filepath.Join(t.TempDir(), "project.yaml")
				data := "keymap:\n  quit: " + bad + "\n  stop: []\n"
				writeKeymapFile(t, global, data)
				rc, err := LoadResolvedConfig(global, "")
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := rc.Config.KeyMap["quit"]; ok {
					t.Fatal("invalid global action survived")
				}
				if keys, ok := rc.Config.KeyMap["stop"]; !ok || keys == nil || len(keys) != 0 {
					t.Fatal("valid sibling lost")
				}
				if ds := keymapTestDiagnostics(rc.Diagnostics); len(ds) != 1 || ds[0].Line != 2 || !strings.Contains(ds[0].Fallback, "built-in defaults") {
					t.Fatal(ds)
				}
				cfg, err := LoadConfigFromPath(global)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(cfg.KeyMap, rc.Config.KeyMap) {
					t.Fatal("global API mismatch")
				}
				writeKeymapFile(t, global, "keymap: {quit: "+inherited+"}\n")
				writeKeymapFile(t, project, data)
				rc, err = LoadResolvedConfig(global, project)
				if err != nil {
					t.Fatal(err)
				}
				base, err := LoadConfigFromPath(global)
				if err != nil {
					t.Fatal(err)
				}
				_, merged, err := MergeProjectConfig(base, project)
				if err != nil {
					t.Fatal(err)
				}
				direct, err := mergeConfigOverrideData(base, []byte(data), project, nil)
				if err != nil {
					t.Fatal(err)
				}
				for _, got := range []*Config{rc.Config, merged, direct} {
					if !reflect.DeepEqual(got.KeyMap["quit"], base.KeyMap["quit"]) {
						t.Fatalf("inheritance lost: %#v", got.KeyMap)
					}
					if keys, ok := got.KeyMap["stop"]; !ok || keys == nil || len(keys) != 0 {
						t.Fatal("valid sibling lost")
					}
				}
				ds := keymapTestDiagnostics(rc.Diagnostics)
				if len(ds) != 1 || ds[0].Line != 2 || ds[0].File != project || !strings.Contains(ds[0].Fallback, keymapMessagePath(global)+":1") {
					t.Fatal(ds)
				}
			})
		}
	}
}

func TestKeymapStructureStillRejectsInvalidReferences(t *testing.T) {
	for _, data := range []string{
		"keymap: &self {quit: *self}",
		"keymap: {quit: &self [*self]}",
		"keymap: {<<: text}",
		"keymap: {<<: [[{quit: []}]]}",
		"model_templates: {maps: &maps [{quit: []}]}\nkeymap: {<<: *maps}",
		"keymap: {missing_action: {<<: text}}",
	} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeKeymapFile(t, path, data)
			if _, err := LoadResolvedConfig(path, ""); err == nil {
				t.Fatal("structural failure was ignored")
			}
			if _, err := mergeConfigOverrideData(DefaultConfig(), []byte(data), path, nil); err == nil {
				t.Fatal("direct merge ignored structural failure")
			}
		})
	}
	for _, data := range []string{
		"keymap: {quit: [q], quit: []}",
		"keymap: {quit: {value: a, value: b}}",
	} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(data), &cfg); err == nil {
			t.Fatal("duplicate was ignored")
		}
	}
}

func TestKeymapUnknownActionDiagnosticIsNotRepeated(t *testing.T) {
	layer, err := inspectKeymapLayer("config.yaml", []byte("keymap: {unknown: []}"), OriginLayerGlobal)
	if err != nil {
		t.Fatal(err)
	}
	ds := keymapDiagnostics(DefaultConfig(), nil, []keymapLayer{layer}, nil)
	if len(ds) != 1 || strings.Count(ds[0].Message, "declaration ignored") != 1 || ds[0].Fallback == "" {
		t.Fatal(ds)
	}
}
