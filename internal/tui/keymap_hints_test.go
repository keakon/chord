package tui

import (
	"strings"
	"testing"

	"github.com/keakon/x/ansi"
)

func TestWelcomeKeyHintsDisabled(t *testing.T) {
	km := KeyMap{}
	got := strings.Join(welcomeKeyHints(ModeInsert, km), "\n")
	if got != "Send shortcut is unbound; configure keymap.insert_submit to send.\nterminal paste: text" {
		t.Fatalf("hints = %q", got)
	}
	if got := welcomeKeyHints(ModeNormal, km); len(got) != 0 {
		t.Fatalf("normal hints = %q", got)
	}
	km = DefaultKeyMap()
	km.InsertEscape = nil
	km.InsertSubmit = nil
	km.HelpToggle = nil
	got = strings.Join(welcomeKeyHints(ModeInsert, km), "\n")
	for _, forbidden := range []string{"normal mode", "for help", "/help", "Press  ", "()"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("unexpected %q in %q", forbidden, got)
		}
	}
	m := NewModelWithSize(&sessionControlAgent{}, 120, 40)
	m.SetKeyMap(KeyMap{})
	rendered := ansi.Strip(m.View().Content)
	if !strings.Contains(rendered, "Send shortcut is unbound") {
		t.Fatalf("welcome render missing unbound hint: %s", rendered)
	}
}

func TestHelpGroupsEffectiveBindings(t *testing.T) {
	km := KeyMap{}
	km.ScrollToTopSeq = []string{"f1", "g"}
	km.ScrollToBottom = []string{"f2"}
	km.ForkSession = []string{"f3"}
	km.HelpToggle = []string{"alt+h"}
	km.InsertAttachFile = []string{"alt+a"}
	var all []string
	for _, group := range km.HelpGroups() {
		for _, b := range group.Bindings {
			if len(b.Keys) == 0 {
				t.Fatalf("empty binding: %+v", b)
			}
			all = append(all, b.Keys...)
			if group.Title == "Help" && b.Help == "close" && keysDisplay(b.Keys) != "esc / q / ? / shift+/" {
				t.Fatalf("help close = %q", b.Keys)
			}
		}
	}
	joined := "|" + strings.Join(all, "|") + "|"
	for _, want := range []string{"f1 f1", "g g", "[count] f1 f1", "[count] g g", "[count] f2", "f3 f3", "alt+a", "yy", "[count]yy", "dd / [count]dd", "[count]"} {
		if !strings.Contains(joined, "|"+want+"|") {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, bad := range []string{"[count] ", "gg", "ee"} {
		if strings.Contains(joined, "|"+bad+"|") {
			t.Errorf("unexpected %q", bad)
		}
	}
	m := NewModel(nil)
	m.SetKeyMap(KeyMap{})
	if got := strings.Join(m.helpLines(120), "\n"); strings.Contains(got, "completes a visible") {
		t.Fatal("disabled submit still advertised")
	}
}

func TestEscHintUsesActualEscapeBinding(t *testing.T) {
	for _, keys := range [][]string{nil, {}, {"alt+e"}, {"esc"}} {
		m := NewModel(nil)
		km := DefaultKeyMap()
		km.InsertEscape = keys
		m.SetKeyMap(km)
		want := ""
		if len(keys) > 0 && keys[0] == "esc" {
			want = "normal mode"
		}
		if got := m.nextEscHint(); got != want {
			t.Fatalf("keys=%q hint=%q want=%q", keys, got, want)
		}
		m.mode = ModeHelp
		if got := m.nextEscHint(); got != "close help" {
			t.Fatalf("fixed overlay hint=%q", got)
		}
	}
}
