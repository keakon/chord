package keybinding

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"
)

func TestNormalizeKey(t *testing.T) {
	tests := map[string]string{
		"g": "g", "G": "G", "?": "?", "+": "+", "ctrl++": "ctrl++",
		"ctrl+shift+left": "ctrl+shift+left", "super+hyper+meta+shift+alt+ctrl+a": "ctrl+alt+shift+meta+hyper+super+a",
		" ": "space", "alt+ ": "alt+space", "f1": "f1", "f63": "f63", "界": "界", "e\u0301": "e\u0301", "👩‍💻": "👩‍💻", "✈️": "✈️",
		"ctrl+i": "ctrl+i", "tab": "tab", "ctrl+m": "ctrl+m", "enter": "enter", "ctrl+[": "ctrl+[", "esc": "esc", "shift+a": "shift+a", "A": "A",
	}
	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			got, err := NormalizeKey(raw)
			if err != nil || got != want {
				t.Fatalf("NormalizeKey(%q) = %q, %v; want %q", raw, got, err, want)
			}
		})
	}
	for _, raw := range []string{"", "ctrl+", "ctrl+ctrl+a", "alt+ctrl+alt+a", "unknown+a", "capslock+a", "f64", "f01", "escape", "return", "gg", " q", "q ", "\n", "\t", "\x00", "\x7f", "\xff", "\u200d", "\u202e"} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			if key, err := NormalizeKey(raw); err == nil {
				t.Fatalf("accepted %q as %q", raw, key)
			}
		})
	}
}

func TestTerminalKeyContract(t *testing.T) {
	tests := []struct {
		key  uv.Key
		want string
	}{
		{uv.Key{Code: 'a', Mod: uv.ModCtrl | uv.ModAlt | uv.ModShift | uv.ModMeta | uv.ModHyper | uv.ModSuper}, "ctrl+alt+shift+meta+hyper+super+a"},
		{uv.Key{Code: uv.KeySpace}, "space"},
		{uv.Key{Code: uv.KeyLeft, Mod: uv.ModCtrl | uv.ModShift}, "ctrl+shift+left"},
		{uv.Key{Code: '+', Mod: uv.ModCtrl}, "ctrl++"},
		{uv.Key{Code: 'G', Text: "G"}, "G"},
		{uv.Key{Code: uv.KeyExtended, Text: "👩‍💻"}, "👩‍💻"},
		{uv.Key{Code: uv.KeyEscape}, "esc"},
		{uv.Key{Code: uv.KeyF63}, "f63"},
		{uv.Key{Code: uv.KeyMediaFastForward}, "mediafastforward"},
		{uv.Key{Code: uv.KeyIsoLevel3Shift}, "isolevel3shift"},
	}
	for _, tt := range tests {
		if got := tt.key.Keystroke(); got != tt.want {
			t.Fatalf("Keystroke = %q, want %q", got, tt.want)
		}
		event := tea.KeyPressMsg(tt.key)
		if got := event.String(); got != tt.want {
			t.Fatalf("KeyPressMsg.String = %q, want %q", got, tt.want)
		}
		if got, err := NormalizeKey(tt.want); got != tt.want || err != nil {
			t.Fatalf("NormalizeKey = %q, %v", got, err)
		}
	}
	// Exercise every named key emitted by the terminal dependency, including
	// keypad aliases and the complete function-key range.
	for code := uv.KeyExtended + 1; code <= uv.KeyIsoLevel5Shift; code++ {
		name := (uv.Key{Code: code}).Keystroke()
		if _, err := NormalizeKey(name); err != nil {
			t.Errorf("terminal key %q rejected: %v", name, err)
		}
	}
}
