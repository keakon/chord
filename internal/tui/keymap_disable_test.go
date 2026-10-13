package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/config"
)

func TestKeymapDisableFromYAMLToModel(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.yaml")
	project := filepath.Join(dir, "project.yaml")
	for path, data := range map[string]string{global: "keymap:\n  quit: [Q]\n  insert_newline: [alt+n]\n", project: "keymap:\n  quit: []\n  insert_newline: []\n  insert_undo: []\n  scroll_to_top_seq: []\n  fork_session: []\n"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.LoadConfigFromPath(global)
	if err != nil {
		t.Fatal(err)
	}
	_, cfg, err = config.MergeProjectConfig(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModelWithSize(&sessionControlAgent{}, 100, 24)
	m.SetKeyMap(KeyMapFromConfig(cfg.KeyMap))
	m.insertComposerText("draft")
	for _, key := range []tea.Key{{Code: 'z', Mod: tea.ModCtrl}, {Code: 'j', Mod: tea.ModCtrl}, {Code: tea.KeyEnter, Mod: tea.ModShift}, {Code: 'n', Mod: tea.ModAlt}} {
		m.handleInsertKey(tea.KeyPressMsg(key))
	}
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("disabled input action changed draft: %q", got)
	}
	m.mode = ModeNormal
	for _, r := range "Qqggee" {
		m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		if m.chord.active() || !m.quit.at.IsZero() {
			t.Fatalf("disabled key %c changed chord/quit state", r)
		}
	}
	for _, group := range m.keyMap.HelpGroups() {
		if group.Title != "Normal Mode" && group.Title != "Insert Mode" {
			continue
		}
		for _, b := range group.Bindings {
			for _, bad := range []string{"quit", "undo composer edit", "insert newline", "fork from user message", "jump to first visible message"} {
				if strings.HasPrefix(b.Help, bad) {
					t.Fatalf("disabled action in help: %+v", b)
				}
			}
		}
	}
	m.mode = ModeHelp
	m.handleHelpKey(tea.KeyPressMsg(tea.Key{Code: 'q', Text: "q"}))
	if m.mode == ModeHelp {
		t.Fatal("fixed help q did not close")
	}
	m.mode = ModeNormal
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if m.quit.at.IsZero() {
		t.Fatal("fixed ctrl+c did not start quit confirmation")
	}
}

func TestKeymapDisabledNewlineIndependentInput(t *testing.T) {
	m := NewModel(nil)
	km := DefaultKeyMap()
	km.InsertNewline = nil
	m.SetKeyMap(km)
	m.handleNonKeyInputMsg(tea.PasteMsg{Content: "first\nsecond"})
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatalf("multiline paste lost newline: %q", m.input.Value())
	}
	dialog := newDialogTextarea(40, 1, 5, "draft")
	dialog, _ = dialog.Update(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}))
	if dialog.Value() != "draft\n" {
		t.Fatalf("dialog newline = %q", dialog.Value())
	}
	km.InsertNewline = []string{"alt+n"}
	m.SetKeyMap(km)
	m.input.SetValue("draft")
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'j', Mod: tea.ModCtrl}))
	if m.input.Value() != "draft" {
		t.Fatal("old newline binding survived remapping")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'n', Mod: tea.ModAlt}))
	if !strings.Contains(m.input.Value(), "\n") {
		t.Fatal("remapped newline did not work")
	}
}

func TestKeymapAllDisabled(t *testing.T) {
	km := DefaultKeyMap()
	v := reflect.ValueOf(&km).Elem()
	for _, field := range v.Fields() {
		field.SetZero()
	}
	m := NewModelWithSize(&sessionControlAgent{}, 100, 24)
	m.SetKeyMap(km)
	if !reflect.DeepEqual(m.keyMap, km) {
		t.Fatal("disabled bindings restored defaults")
	}
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'g', Text: "g"}))
	if m.input.Value() != "g" {
		t.Fatal("disabled navigation swallowed typed character")
	}
	_ = m.View()
	_ = m.helpLines(100)
}

func TestKeymapDisabledChordsAndFixedCount(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		backend := &sessionControlAgent{}
		m := NewModelWithSize(backend, 100, 24)
		m.mode = ModeNormal
		m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "first", MsgIndex: 0})
		m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "second", MsgIndex: 1})
		m.focusedBlockID = 2
		km := DefaultKeyMap()
		if disabled {
			km.ScrollToTopSeq = nil
			km.ScrollToBottom = nil
			km.ForkSession = nil
			km.NextBlock = nil
		}
		m.SetKeyMap(km)
		press := func(r rune) { m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)})) }
		press('g')
		if (m.chord.op == chordG) == disabled {
			t.Fatalf("disabled=%v chord=%+v", disabled, m.chord)
		}
		press('g')
		if disabled && m.focusedBlockID != 2 {
			t.Fatal("disabled gg moved focus")
		}
		if !disabled && m.focusedBlockID != 1 {
			t.Fatal("enabled gg did not move focus")
		}
		m.focusedBlockID = 2
		press('e')
		if (m.chord.op == chordE) == disabled {
			t.Fatalf("disabled=%v fork chord=%+v", disabled, m.chord)
		}
		press('e')
		wantForks := 1
		if disabled {
			wantForks = 0
		}
		if len(backend.forkMsgIndices) != wantForks {
			t.Fatalf("fork calls=%v", backend.forkMsgIndices)
		}
		if disabled {
			for _, seq := range []string{"1gg", "1G", "2j"} {
				m.focusedBlockID = 2
				for _, r := range seq {
					press(r)
				}
				if m.focusedBlockID != 2 {
					t.Fatalf("disabled %s moved focus", seq)
				}
			}
			for _, op := range []chordOp{chordG, chordE} {
				m.chord = chordState{op: op}
				r := 'g'
				if op == chordE {
					r = 'e'
				}
				press(r)
				if len(backend.forkMsgIndices) != 0 || m.focusedBlockID != 2 {
					t.Fatal("disabled pending chord completed")
				}
			}
		}
		m.input.SetValue("draft")
		press('2')
		press('d')
		if m.chord.count != 2 || m.chord.op != chordD {
			t.Fatalf("fixed counted dd unavailable: %+v", m.chord)
		}
		press('d')
		if m.input.Value() != "" {
			t.Fatal("fixed dd did not clear input")
		}
	}
}

func TestKeymapDisabledStopKeepsNormalEscape(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModel(backend)
	m.mode = ModeNormal
	m.focusedAgentID = "agent-1"
	m.activities["agent-1"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "agent-1"}
	km := DefaultKeyMap()
	km.Stop = nil
	m.SetKeyMap(km)
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModCtrl}))
	if backend.cancelCalls != 0 {
		t.Fatal("disabled Stop cancelled turn")
	}
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if backend.cancelCalls != 1 {
		t.Fatal("fixed normal Escape did not cancel")
	}
}

func TestKeymapSequenceUsesAnyConfiguredMember(t *testing.T) {
	backend := &sessionControlAgent{}
	m := NewModelWithSize(backend, 100, 24)
	m.mode = ModeNormal
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "first", MsgIndex: 0})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "second", MsgIndex: 1})
	m.focusedBlockID = 2
	km := DefaultKeyMap()
	km.ScrollToTopSeq = []string{"f1", "f2"}
	km.ForkSession = []string{"f3", "f4"}
	m.SetKeyMap(km)
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF1}))
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF2}))
	if m.focusedBlockID != 1 || m.chord.active() {
		t.Fatal("mixed navigation bindings did not complete sequence")
	}
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF3}))
	m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyF4}))
	if len(backend.forkMsgIndices) != 1 || backend.forkMsgIndices[0] != 0 {
		t.Fatalf("mixed fork bindings = %v", backend.forkMsgIndices)
	}
	for _, r := range "2yy" {
		m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	if m.chord.active() {
		t.Fatal("fixed counted copy did not complete")
	}
}
