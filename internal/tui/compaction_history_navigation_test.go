package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"

	"github.com/keakon/chord/internal/keybinding"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestCompactionHistoryShortcutRoundTrip(t *testing.T) {
	for name, sequences := range map[string][2]string{
		"xterm":       {"\x1b[1;3D", "\x1b[1;3C"},
		"escape":      {"\x1b\x1b[D", "\x1b\x1b[C"},
		"word-motion": {"\x1bb", "\x1bf"},
		"kitty":       {"\x1b[57350;3u", "\x1b[57351;3u"},
	} {
		t.Run(name, func(t *testing.T) {
			earlier := decodeHistoryShortcut(t, sequences[0])
			later := decodeHistoryShortcut(t, sequences[1])
			for name, mode := range map[string]Mode{"normal": ModeNormal, "insert": ModeInsert} {
				t.Run(name, func(t *testing.T) {
					testCompactionHistoryShortcutRoundTrip(t, mode, earlier, later)
				})
			}
		})
	}
}

func decodeHistoryShortcut(t *testing.T, sequence string) tea.KeyPressMsg {
	t.Helper()
	decoder := uv.EventDecoder{}
	n, event := decoder.Decode([]byte(sequence))
	key, ok := event.(uv.KeyPressEvent)
	if !ok || n != len(sequence) {
		t.Fatalf("decode %q: consumed %d bytes, event %T", sequence, n, event)
	}
	return tea.KeyPressMsg(key)
}

func testCompactionHistoryShortcutRoundTrip(t *testing.T, mode Mode, earlier, later tea.KeyPressMsg) {
	t.Helper()
	dir := compactionHistorySessionDir(t, 2, 5)
	backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
	m := NewModelWithSize(backend, 100, 30)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = mode
	if mode == ModeInsert {
		m.input.Focus()
	} else {
		m.input.Suspend()
	}
	updateKey := func(msg tea.KeyMsg) tea.Cmd {
		_, cmd := m.Update(msg)
		return cmd
	}
	liveViewport := m.viewport

	updateKey(later)
	if m.compactionHistory.active {
		t.Fatal("Alt+Right in the live session must not open history")
	}
	m.input.SetValue("unfinished draft")
	m.input.CursorEnd()
	line, column := m.input.Line(), m.input.Column()
	driveCompactionHistoryCmd(t, &m, updateKey(earlier))
	if !m.compactionHistory.active || m.compactionHistory.current != 1 {
		t.Fatal("Alt+Left did not open the newest snapshot")
	}
	if m.input.Focused() || m.input.Line() != line || m.input.Column() != column {
		t.Fatal("opening history must suspend the composer without moving its cursor")
	}
	frame := terminaltext.StripANSI(m.View().Content)
	for _, hint := range []string{"HISTORY 2/2", "esc ⇢ live"} {
		if !strings.Contains(frame, hint) {
			t.Fatalf("history frame lacks %q: %s", hint, frame)
		}
	}
	driveCompactionHistoryCmd(t, &m, updateKey(earlier))
	if m.compactionHistory.current != 0 {
		t.Fatal("Alt+Left did not reach the earlier snapshot")
	}
	if cmd := updateKey(earlier); cmd != nil || m.compactionHistory.current != 0 || !m.compactionHistory.active {
		t.Fatal("stepping past the earliest snapshot must keep it open")
	}
	driveCompactionHistoryCmd(t, &m, updateKey(later))
	if m.compactionHistory.current != 1 {
		t.Fatal("Alt+Right did not reach the newer snapshot")
	}
	updateKey(later)
	if m.compactionHistory.active || m.compactionHistory.viewport != nil {
		t.Fatal("Alt+Right past the newest snapshot did not release the browser")
	}
	if m.mode != mode || m.input.Value() != "unfinished draft" || m.viewport != liveViewport {
		t.Fatal("history navigation changed the live mode, draft, or viewport")
	}
	if m.input.Line() != line || m.input.Column() != column || m.input.Focused() != (mode == ModeInsert) {
		t.Fatal("returning to the live session did not restore the cursor and focus")
	}
	if len(backend.submittedInputs) != 0 {
		t.Fatal("history navigation submitted a message")
	}
	updateKey(later)
	if m.compactionHistory.active {
		t.Fatal("Alt+Right reopened history after returning to the live session")
	}
}

func TestCompactionHistoryShortcutCancelsPendingNavigation(t *testing.T) {
	for _, stage := range []string{"scan", "load", "switch"} {
		t.Run(stage, func(t *testing.T) {
			dir := compactionHistorySessionDir(t, 2, 5)
			backend := &compactionHistorySessionAgent{sessionControlAgent: &sessionControlAgent{}, dir: dir}
			m := NewModelWithSize(backend, 100, 30)
			t.Cleanup(func() { _ = m.Close() })
			m.mode = ModeNormal
			earlier := tea.KeyPressMsg{Code: tea.KeyLeft, Mod: tea.ModAlt}
			later := tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt}
			pending := m.handleKeyMsg(earlier)
			if stage != "scan" {
				pending = m.handleCompactionHistoryScanned(pending().(compactionHistoryScannedMsg))
			}
			if stage == "switch" {
				if cmd := m.handleKeyMsg(earlier); cmd != nil || m.compactionHistory.pendingIndex != 0 {
					t.Fatal("pending navigation did not retain the earlier target")
				}
				if cmd := m.handleKeyMsg(later); cmd != nil || m.compactionHistory.pendingIndex != 1 {
					t.Fatal("pending navigation did not retain the newer target")
				}
			}
			m.handleKeyMsg(later)
			if m.compactionHistory.active {
				t.Fatal("Alt+Right did not leave a pending history read")
			}
			next, handled := m.handleCompactionHistoryMessage(pending())
			if !handled || next != nil || m.compactionHistory.active || m.compactionHistory.viewport != nil {
				t.Fatal("a late result revived the closed browser")
			}
		})
	}
}

func TestCompactionHistoryShortcutsLeaveSearchEditingKeysAlone(t *testing.T) {
	for name, keys := range map[string][2]rune{"arrows": {tea.KeyLeft, tea.KeyRight}, "word-motion": {'b', 'f'}} {
		t.Run(name, func(t *testing.T) {
			testCompactionHistorySearchEditingKeys(t, keys)
		})
	}
}

func testCompactionHistorySearchEditingKeys(t *testing.T, keys [2]rune) {
	t.Helper()
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = ModeNormal
	driveCompactionHistoryCmd(t, m, m.openCompactionHistory())
	m.beginCompactionHistorySearch()
	h := &m.compactionHistory
	h.searchInput.SetValue("first second")
	h.searchInput.CursorEnd()
	m.handleKeyMsg(tea.KeyPressMsg{Code: keys[0], Mod: tea.ModAlt})
	if !h.active || !h.searching || h.current != 0 || h.searchInput.Position() >= len("first second") {
		t.Fatal("Alt+Left did not stay with the search editor")
	}
	m.handleKeyMsg(tea.KeyPressMsg{Code: keys[1], Mod: tea.ModAlt})
	if !h.active || !h.searching || h.current != 0 || h.searchInput.Value() != "first second" {
		t.Fatal("Alt+Right left the search editor")
	}
	m.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !h.active || h.searching {
		t.Fatal("Esc did not leave only the search editor")
	}
	m.handleKeyMsg(tea.KeyPressMsg{Code: tea.KeyEscape})
	if h.active {
		t.Fatal("Esc did not return to the live session")
	}
}

func TestCompactionHistoryShortcutOverrides(t *testing.T) {
	for name, mode := range map[string]Mode{"normal": ModeNormal, "insert": ModeInsert} {
		t.Run(name, func(t *testing.T) {
			testCompactionHistoryShortcutOverrides(t, mode)
		})
	}
}

func testCompactionHistoryShortcutOverrides(t *testing.T, mode Mode) {
	t.Helper()
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	m.mode = mode
	m.keyMap = KeyMapFromConfig(map[string][]string{
		string(keybinding.ActionCompactionHistory):     {"alt+["},
		string(keybinding.ActionCompactionHistoryNext): {"alt+]"},
	})
	for _, code := range []rune{tea.KeyLeft, 'b'} {
		m.handleKeyMsg(tea.KeyPressMsg{Code: code, Mod: tea.ModAlt})
		if m.compactionHistory.active {
			t.Fatal("the overridden default still opens history")
		}
	}
	driveCompactionHistoryCmd(t, m, m.handleKeyMsg(tea.KeyPressMsg{Code: '[', Mod: tea.ModAlt}))
	if !m.compactionHistory.active {
		t.Fatal("the configured earlier key did not open history")
	}
	for _, code := range []rune{tea.KeyRight, 'f'} {
		m.handleKeyMsg(tea.KeyPressMsg{Code: code, Mod: tea.ModAlt})
		if !m.compactionHistory.active {
			t.Fatal("the overridden default still closes history")
		}
	}
	m.handleKeyMsg(tea.KeyPressMsg{Code: ']', Mod: tea.ModAlt})
	if m.compactionHistory.active {
		t.Fatal("the configured later key did not return to the live session")
	}
	m.keyMap = KeyMapFromConfig(map[string][]string{
		string(keybinding.ActionCompactionHistory):     {},
		string(keybinding.ActionCompactionHistoryNext): {},
	})
	for _, code := range []rune{tea.KeyLeft, 'b'} {
		m.handleKeyMsg(tea.KeyPressMsg{Code: code, Mod: tea.ModAlt})
		if m.compactionHistory.active {
			t.Fatal("a disabled shortcut opened history")
		}
	}
	driveCompactionHistoryCmd(t, m, m.openCompactionHistory())
	for _, code := range []rune{tea.KeyRight, 'f'} {
		m.handleKeyMsg(tea.KeyPressMsg{Code: code, Mod: tea.ModAlt})
		if !m.compactionHistory.active {
			t.Fatal("a disabled shortcut closed history")
		}
	}
}

func TestCompactionHistoryHasNoSlashCompletion(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	t.Cleanup(func() { _ = m.Close() })
	for _, completion := range m.getSlashCompletions("/") {
		if completion.Cmd == "/compaction-history" {
			t.Fatal("history navigation must not add a slash command")
		}
	}
}

func TestCompactionHistoryShortcutDoesNotBypassSources(t *testing.T) {
	dir := compactionHistorySessionDir(t, 3)
	m, block := compactionHistoryModel(t, dir, compactionCheckpointContent("/sample/history-2.md", "/sample/history-3.md"))
	t.Cleanup(func() { _ = m.Close() })
	driveCompactionHistoryCmd(t, m, m.openCompactionSummaryHistory(block))
	for _, code := range []rune{tea.KeyLeft, tea.KeyRight, 'b', 'f'} {
		m.handleKeyMsg(tea.KeyPressMsg{Code: code, Mod: tea.ModAlt})
		if !m.compactionHistory.sourceListActive || m.compactionHistory.pendingIndex != -1 {
			t.Fatal("a navigation shortcut bypassed the referenced source list")
		}
	}
}

func TestCompactionHistoryShortcutLeavesDialogsInControl(t *testing.T) {
	m, _ := newCompactionHistoryIsolationModel(t)
	t.Cleanup(func() { _ = m.Close() })
	for _, mode := range []Mode{ModeSearch, ModeInputHistorySearch, ModeConfirm, ModeQuestion, ModeHelp, ModeSessionSelect} {
		m.mode = mode
		for _, code := range []rune{tea.KeyLeft, 'b'} {
			if m.compactionHistoryActionFires(tea.KeyPressMsg{Code: code, Mod: tea.ModAlt}) {
				t.Fatalf("history shortcut intercepted mode %v", mode)
			}
		}
	}
}
