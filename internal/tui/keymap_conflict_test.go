package tui

import (
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/keybinding"
)

func TestKeymapConflictStopPrecedesUndo(t *testing.T) {
	backend := &sessionControlAgent{cancelResult: true}
	m := NewModelWithSize(backend, 80, 24)
	m.SetKeyMap(KeyMapFromConfig(map[string][]string{string(keybinding.ActionStop): {"alt+x"}, string(keybinding.ActionInsertUndo): {"alt+x"}}))
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModAlt}))
	if backend.cancelCalls != 1 || m.input.Value() != "a" {
		t.Fatal("Stop did not precede Undo")
	}
	m.keyMap.Stop = nil
	m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: 'x', Mod: tea.ModAlt}))
	if m.input.Value() != "" {
		t.Fatal("Undo positive control did not restore draft")
	}
}

func TestKeymapConflictStopPrecedesRoleExceptCtrlM(t *testing.T) {
	for _, code := range []rune{'x', 'm'} {
		t.Run(string(code), func(t *testing.T) {
			backend := &sessionControlAgent{cancelResult: true, currentRole: "builder", availableRoles: []string{"builder", "planner"}}
			m := NewModelWithSize(backend, 80, 24)
			m.mode = ModeNormal
			binding := "ctrl+" + string(code)
			m.SetKeyMap(KeyMapFromConfig(map[string][]string{string(keybinding.ActionStop): {binding}, string(keybinding.ActionSwitchRole): {binding}}))
			m.activities["main"] = agent.AgentActivityEvent{AgentID: "main", Type: agent.ActivityStreaming}
			m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl}))
			want := 1
			if code == 'm' {
				want = 0
			}
			if backend.cancelCalls != want || backend.currentRole != "builder" {
				t.Fatalf("cancel=%d role=%s", backend.cancelCalls, backend.currentRole)
			}
			if code == 'x' {
				m.keyMap.Stop = nil
				delete(m.activities, "main")
				m.handleKeyMsg(tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl}))
				if backend.currentRole != "planner" {
					t.Fatal("role positive control failed")
				}
			}
		})
	}
}

func TestKeymapConflictGlobalEntrypoints(t *testing.T) {
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModSuper} {
		t.Run((tea.Key{Code: 'c', Mod: mod}).String(), func(t *testing.T) {
			m := NewModelWithSize(nil, 80, 24)
			key := tea.KeyPressMsg(tea.Key{Code: 'c', Mod: mod})
			m.SetKeyMap(KeyMapFromConfig(map[string][]string{string(keybinding.ActionInsertEscape): {key.String()}}))
			m.input.SetValue("Sample draft")
			cmd := m.handleKeyMsg(key)
			if m.mode != ModeInsert || cmd == nil {
				t.Fatal("fixed entry did not precede mode switch")
			}
			if mod == tea.ModCtrl && m.quit.by != "ctrl+c" {
				t.Fatal("Ctrl+C did not open quit confirmation")
			}
			// Direct mode dispatch proves the same event would invoke the configured
			// action in the absence of the global fixed entry.
			m.handleModeKey(key)
			if m.mode != ModeNormal {
				t.Fatal("configured mode switch positive control failed")
			}
		})
	}
}
