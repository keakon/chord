package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/keybinding"
)

func TestKeymapRawValidation(t *testing.T) {
	cases := []struct {
		name, body string
		want       KeyMapConfig
		issue      string
	}{
		{"empty", "{}", KeyMapConfig{}, ""},
		{"disabled", "{quit: []}", KeyMapConfig{"quit": {}}, ""},
		{"normalize", "{quit: ['alt+ctrl+q', 'ctrl+alt+q', ' ', space]}", KeyMapConfig{"quit": {"ctrl+alt+q", "space"}}, ""},
		{"number", "{quit: [1], help_toggle: ['?']}", KeyMapConfig{"help_toggle": {"?"}}, "keymap.quit[0]"},
		{"boolean", "{quit: [true]}", KeyMapConfig{}, "keymap.quit[0]"},
		{"null element", "{quit: [Q, null]}", KeyMapConfig{}, "keymap.quit[1]"},
		{"mapping element", "{quit: [Q, {}]}", KeyMapConfig{}, "keymap.quit[1]"},
		{"null action", "{quit: null}", KeyMapConfig{}, "keymap.quit"},
		{"scalar action", "{quit: q}", KeyMapConfig{}, "keymap.quit"},
		{"mapping action", "{quit: {}}", KeyMapConfig{}, "keymap.quit"},
		{"quoted number", "{quit: ['1', !!str 2]}", KeyMapConfig{"quit": {"1", "2"}}, ""},
		{"unknown", "{missing_action: [null]}", KeyMapConfig{}, "keymap.missing_action"},
		{"invalid key", "{quit: [Q, escape, q]}", KeyMapConfig{}, "keymap.quit[1]"},
		{"null field", "null", KeyMapConfig{}, "keymap"},
		{"scalar field", "q", KeyMapConfig{}, "keymap"},
		{"sequence field", "[]", KeyMapConfig{}, "keymap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layer, err := inspectKeymapLayer("sample.yaml", []byte("keymap: "+tc.body+"\n"), OriginLayerGlobal)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(layer.Bindings, tc.want) {
				t.Fatalf("bindings = %#v, want %#v", layer.Bindings, tc.want)
			}
			if tc.issue == "" {
				if len(layer.Issues) != 0 {
					t.Fatalf("issues = %#v", layer.Issues)
				}
			} else if len(layer.Issues) != 1 || layer.Issues[0].Path != tc.issue {
				t.Fatalf("issues = %#v", layer.Issues)
			}
		})
	}
}

func TestKeymapLayeredLoad(t *testing.T) {
	cases := []struct {
		name, global, project string
		want                  []string
		warnings              int
	}{
		{"global binding", "[Q]", "", []string{"Q"}, 0},
		{"global disabled", "[]", "", []string{}, 0},
		{"project disables", "[Q]", "[]", []string{}, 0},
		{"project rebinds", "[]", "[q]", []string{"q"}, 0},
		{"invalid keeps disabled", "[]", "[Q, null]", []string{}, 1},
		{"invalid keeps binding", "[Q]", "[q, {}]", []string{"Q"}, 1},
		{"invalid global final project", "[null]", "[Q]", []string{"Q"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			global := filepath.Join(dir, "global.yaml")
			project := filepath.Join(dir, "project.yaml")
			writeKeymapFile(t, global, "keymap:\n  quit: "+tc.global+"\n")
			projectText := "keymap: {}\n"
			if tc.project != "" {
				projectText = "keymap:\n  quit: " + tc.project + "\n"
			}
			writeKeymapFile(t, project, projectText)
			rc, err := LoadResolvedConfig(global, project)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rc.Config.KeyMap["quit"], tc.want) {
				t.Fatalf("quit = %#v, want %#v", rc.Config.KeyMap["quit"], tc.want)
			}
			warnings := keymapTestDiagnostics(rc.Diagnostics)
			if len(warnings) != tc.warnings {
				t.Fatalf("warnings = %#v", warnings)
			}
			for _, d := range warnings {
				if d.Severity != DiagnosticSeverityWarning || !d.Continues || d.Line != 2 || d.Col == 0 || !strings.Contains(d.Message, keymapBindingText(tc.want)) {
					t.Fatalf("diagnostic = %#v", d)
				}
				if !strings.Contains(d.Message, d.Fallback) {
					t.Fatalf("fallback absent: %#v", d)
				}
			}
			cfg, err := LoadConfigFromPath(global)
			if err != nil {
				t.Fatal(err)
			}
			_, merged, err := MergeProjectConfig(cfg, project)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(merged.KeyMap, rc.Config.KeyMap) {
				t.Fatalf("API mismatch: %#v / %#v", merged.KeyMap, rc.Config.KeyMap)
			}
			if tc.name == "invalid global final project" {
				if warnings[0].File != global || !strings.Contains(warnings[0].Message, keymapMessagePath(project)+":2") {
					t.Fatalf("incorrect final source: %#v", warnings[0])
				}
			}
		})
	}
}

func writeKeymapFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func keymapTestDiagnostics(diagnostics []Diagnostic) []Diagnostic {
	var result []Diagnostic
	for _, d := range diagnostics {
		if d.Scope == keymapDiagnosticScope {
			result = append(result, d)
		}
	}
	return result
}

func TestKeymapAliasesAndMerge(t *testing.T) {
	data := []byte("model_templates:\n  keys: &keys [Q, null]\n  defaults: &defaults\n    quit: [g]\n    help_toggle: ['?']\n  key: &key Z\nkeymap:\n  <<: *defaults\n  quit: *keys\n  stop: [*key]\n")
	layer, err := inspectKeymapLayer("sample.yaml", data, OriginLayerProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := layer.Bindings["quit"]; ok {
		t.Fatal("invalid explicit declaration revived merge value")
	}
	if !reflect.DeepEqual(layer.Bindings["stop"], []string{"Z"}) {
		t.Fatal(layer.Bindings)
	}
	if layer.Origins[keybinding.ActionHelpToggle].Line != 5 {
		t.Fatal(layer.Origins)
	}
	if len(layer.Issues) != 1 || layer.Issues[0].Node.Line != 2 {
		t.Fatalf("alias error must locate target element: %#v", layer.Issues)
	}
	base := DefaultConfig()
	base.KeyMap = KeyMapConfig{"quit": {}}
	merged, err := mergeConfigOverrideData(base, data, "sample.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if keys, ok := merged.KeyMap["quit"]; !ok || keys == nil || len(keys) != 0 {
		t.Fatalf("inherited disable lost: %#v", merged.KeyMap)
	}
	empty := strings.Replace(string(data), "quit: *keys", "quit: []", 1)
	parsed, err := inspectKeymapLayer("sample.yaml", []byte(empty), OriginLayerProject)
	if err != nil {
		t.Fatal(err)
	}
	if keys, ok := parsed.Bindings["quit"]; !ok || keys == nil || len(keys) != 0 {
		t.Fatalf("explicit disable lost: %#v", parsed.Bindings)
	}
}

func TestKeymapStructuralErrors(t *testing.T) {
	for _, data := range []string{"keymap: {<<: text}", "keymap: &keys {quit: *keys}", "keymap: {quit: [Q], quit: [q]}"} {
		var cfg Config
		if err := yaml.Unmarshal([]byte(data), &cfg); err == nil {
			t.Fatalf("accepted malformed keymap: %s", data)
		}
	}
}

func TestKeymapTopLevelInvalidInheritance(t *testing.T) {
	for _, value := range []string{"null", "[]", "q", "{}"} {
		t.Run(value, func(t *testing.T) {
			base := DefaultConfig()
			base.KeyMap = KeyMapConfig{"quit": {}}
			cfg, err := mergeConfigOverrideData(base, []byte("keymap: "+value), "sample.yaml", nil)
			if err != nil {
				t.Fatal(err)
			}
			if keys, ok := cfg.KeyMap["quit"]; !ok || keys == nil || len(keys) != 0 {
				t.Fatalf("lost disabled binding: %#v", cfg.KeyMap)
			}
		})
	}
}

func TestKeymapConflictDiagnostics(t *testing.T) {
	data := []byte("keymap:\n  stop: [ctrl+z]\n  quit: [ctrl+c]\n")
	layer, err := inspectKeymapLayer("sample.yaml", data, OriginLayerGlobal)
	if err != nil {
		t.Fatal(err)
	}
	rc := &ResolvedConfig{}
	rc.addKeymapLayer(layer)
	cfg := DefaultConfig()
	cfg.KeyMap = layer.Bindings
	diagnostics := keymapDiagnostics(cfg, rc.Index, []keymapLayer{layer}, nil)
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	for _, d := range diagnostics {
		if d.Fallback != "" || d.File != "sample.yaml" || d.Line == 0 || !strings.Contains(d.Message, "shadowed") {
			t.Fatalf("diagnostic = %#v", d)
		}
	}
}

func TestKeymapDiagnosticEscaping(t *testing.T) {
	data := []byte("keymap:\n  \"unknown\\naction\": [q]\n  quit: [\"bad\\nkey\"]\n")
	layer, err := inspectKeymapLayer("sample\nfile.yaml", data, OriginLayerGlobal)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.KeyMap = layer.Bindings
	for _, d := range keymapDiagnostics(cfg, nil, []keymapLayer{layer}, nil) {
		if strings.ContainsAny(d.Message, "\n\r\t") {
			t.Fatalf("unescaped message: %q", d.Message)
		}
	}
	if len([]rune(keymapSafeText(strings.Repeat("x", 1000)))) > 125 {
		t.Fatal("unbounded user text")
	}
}

func TestKeymapOriginalOriginsAfterSanitization(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.yaml")
	project := filepath.Join(dir, "project.yaml")
	writeKeymapFile(t, global, "keymap:\n  quit: []\n")
	writeKeymapFile(t, project, "unknown_field: ignored\n\n\nkeymap:\n  quit: [q, null]\n  help_toggle: [h]\n")
	rc, err := LoadResolvedConfig(global, project)
	if err != nil {
		t.Fatal(err)
	}
	warnings := keymapTestDiagnostics(rc.Diagnostics)
	if len(warnings) != 1 || warnings[0].Line != 5 || warnings[0].Col != 13 || warnings[0].File != project || warnings[0].Path != "keymap.quit[1]" {
		t.Fatalf("original issue = %#v", warnings)
	}
	origin, ok := keymapEffectiveOrigin(rc.Index, keybinding.ActionHelpToggle)
	if !ok || origin.Line != 6 || origin.Col != 3 || origin.File != project {
		t.Fatalf("origin = %#v, %v", origin, ok)
	}
	if keys, ok := rc.Config.KeyMap["quit"]; !ok || keys == nil || len(keys) != 0 {
		t.Fatal(rc.Config.KeyMap)
	}
}

func TestKeymapRoundTripAndMissingProject(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.yaml")
	writeKeymapFile(t, global, "model_templates:\n  timeout: &timeout 17\nconfirm_timeout: *timeout\nkeymap:\n  quit: []\n  stop: [null]\n")
	rc, err := LoadResolvedConfig(global, filepath.Join(dir, "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if rc.Config.ConfirmTimeout != 17 {
		t.Fatalf("unrelated alias changed: %d", rc.Config.ConfirmTimeout)
	}
	data, err := yaml.Marshal(rc.Config)
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if keys, ok := cfg.KeyMap["quit"]; !ok || keys == nil || len(keys) != 0 {
		t.Fatalf("disabled lost on round trip: %#v", cfg.KeyMap)
	}
	if _, ok := cfg.KeyMap["stop"]; ok {
		t.Fatalf("invalid action retained: %#v", cfg.KeyMap)
	}
}

func TestKeymapAliasTypeLocationAndMergePrecedence(t *testing.T) {
	data := []byte("model_templates:\n  invalid: &invalid null\n  first: &first {quit: [Q]}\n  second: &second {quit: [g], stop: []}\nkeymap:\n  <<: [*first, *second]\n  help_toggle: *invalid\n")
	layer, err := inspectKeymapLayer("sample.yaml", data, OriginLayerGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(layer.Bindings["quit"], []string{"Q"}) {
		t.Fatal(layer.Bindings)
	}
	if len(layer.Issues) != 1 || layer.Issues[0].Node.Line != 7 {
		t.Fatalf("alias field error must locate usage: %#v", layer.Issues)
	}
	if layer.Origins[keybinding.ActionQuit].Line != 3 {
		t.Fatal(layer.Origins)
	}
}

func TestKeymapDiagnosticOrderAndInheritedSource(t *testing.T) {
	global, err := inspectKeymapLayer("global.yaml", []byte("keymap:\n  quit: [null]\n  stop: [bad_key]\n"), OriginLayerGlobal)
	if err != nil {
		t.Fatal(err)
	}
	project, err := inspectKeymapLayer("project.yaml", []byte("keymap:\n  help_toggle: [null]\n"), OriginLayerProject)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.KeyMap = KeyMapConfig{"quit": {}}
	diagnostics := keymapDiagnostics(cfg, nil, []keymapLayer{global, project}, cfg.KeyMap)
	if len(diagnostics) != 3 {
		t.Fatal(diagnostics)
	}
	if diagnostics[0].Path != "keymap.quit[0]" || diagnostics[1].Path != "keymap.stop[0]" || diagnostics[2].File != "project.yaml" {
		t.Fatal(diagnostics)
	}
	if !strings.Contains(diagnostics[0].Fallback, "disabled from inherited configuration") {
		t.Fatal(diagnostics[0])
	}
}

func TestKeymapDuplicateLoaderPoliciesAndOrigins(t *testing.T) {
	for _, data := range []string{
		"keymap:\n  quit: [ctrl+c]\n  quit: [Q]\n",
		"keymap:\n  quit: [ctrl+c]\nkeymap:\n  quit: [Q]\n",
	} {
		t.Run(strings.ReplaceAll(data, "\n", " "), func(t *testing.T) {
			dir := t.TempDir()
			global := filepath.Join(dir, "global.yaml")
			project := filepath.Join(dir, "project.yaml")
			writeKeymapFile(t, global, data)
			rc, err := LoadResolvedConfig(global, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(rc.Config.KeyMap) != 0 || len(rc.Index.KeyMap) != 0 {
				t.Fatalf("global duplicates retained binding or origin: %#v / %#v", rc.Config.KeyMap, rc.Index.KeyMap)
			}
			if len(rc.Diagnostics) == 0 {
				t.Fatal("global duplicate warning disappeared")
			}
			writeKeymapFile(t, global, "keymap: {}\n")
			writeKeymapFile(t, project, data)
			if _, err := LoadResolvedConfig(global, project); err == nil {
				t.Fatal("resolved project duplicate must remain a parse error")
			}
			_, merged, err := MergeProjectConfig(DefaultConfig(), project)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(merged.KeyMap["quit"], []string{"ctrl+c"}) {
				t.Fatalf("project recovery changed: %#v", merged.KeyMap)
			}
			layer, err := inspectKeymapLayer(project, []byte(data), OriginLayerProject)
			if err != nil {
				t.Fatal(err)
			}
			index := &ResolvedConfig{}
			index.addKeymapLayer(layer)
			origin, ok := keymapEffectiveOrigin(index.Index, keybinding.ActionQuit)
			if !ok || origin.Line != 2 {
				t.Fatalf("recovered source = %#v", origin)
			}
			diagnostics := keymapDiagnostics(merged, index.Index, []keymapLayer{layer}, nil)
			if len(diagnostics) != 1 || diagnostics[0].Line != 2 || !strings.Contains(diagnostics[0].Message, "shadowed") {
				t.Fatalf("recovered conflict = %#v", diagnostics)
			}
		})
	}
}
