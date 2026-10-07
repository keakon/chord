package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/agent"
)

func TestStatusPanelUsesFullWidthAndRestoresComposer(t *testing.T) {
	for _, width := range []int{40, 80, 120, 160} {
		m := NewModelWithSize(newInfoPanelAgent(), width, 24)
		m.input.SetValue("Keep this draft")
		m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "worker", TaskDesc: "Review changes"}}, "main", "builder")
		m.sidebar.UpdateStatus("worker", subAgentStatusError)
		m.openStatusPanel()
		view := m.View()
		if m.layout.infoPanel.Dx() != width || m.layout.main.Dx() != width {
			t.Fatalf("overview at %d columns does not use the full width", width)
		}
		if text := ansi.Strip(view.Content); !strings.Contains(text, "✗ Review changes") || !strings.Contains(text, "STATUS") {
			t.Fatalf("overview omitted runtime state at width %d", width)
		}
		m.handleStatusPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		if m.mode != ModeInsert || m.input.Value() != "Keep this draft" {
			t.Fatal("closing overview lost input mode or the draft")
		}
		m.View()
		if width < rightPanelShowMinWidth && m.layout.infoPanel.Dx() != 0 {
			t.Fatal("narrow view retained the full-screen panel after close")
		}
	}
}

func TestStatusPanelKeyboardAndMouseUseTheVisiblePanel(t *testing.T) {
	backend := newInfoPanelAgent()
	m := NewModelWithSize(backend, 40, 12)
	m.sidebar.Update([]agent.SubAgentInfo{{InstanceID: "worker", TaskDesc: "Review changes"}}, "main", "builder")
	m.openStatusPanel()
	m.View()
	before := m.viewport.offset
	m.handleMouseMsg(tea.MouseWheelMsg{X: 2, Y: 2, Button: tea.MouseWheelDown})
	if m.infoPanelScrollOffset == 0 || m.viewport.offset != before {
		t.Fatal("overview wheel did not scroll its own content")
	}
	m.handleStatusPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m.View()
	if m.statusPanel.section != infoPanelSectionAgents {
		t.Fatalf("first actionable section = %q, want agents", m.statusPanel.section)
	}
	if !strings.Contains(ansi.Strip(m.cachedDirRender.text), "▸ ▼ AGENTS") {
		t.Fatal("keyboard section focus has no shape cue")
	}
	m.handleStatusPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.View()
	if !m.isInfoPanelSectionCollapsed(infoPanelSectionAgents) {
		t.Fatal("Enter did not collapse the selected section")
	}
	m.handleStatusPanelKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m.View()
	// Agents are a transient section and now render after the stable ones, so
	// at this small height the worker row is only on screen at the bottom.
	m.handleStatusPanelKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
	m.View()
	for _, hit := range m.infoPanelHitBoxes {
		if hit.agentID == "worker" {
			y := m.layout.infoPanel.Min.Y + hit.startY - m.infoPanelScrollOffset
			m.handleMouseMsg(tea.MouseClickMsg{X: 4, Y: y, Button: tea.MouseLeft})
			if m.focusedAgentID != "worker" || backend.focused != "worker" {
				t.Fatal("click did not focus the visible worker")
			}
			return
		}
	}
	t.Fatal("worker has no visible click target")
}

func TestStatusCommandIsLocalEvenWhenAgentIsBusy(t *testing.T) {
	backend := &loopBusyAgentStub{}
	m := NewModelWithSize(backend, 80, 24)
	m.input.SetValue(statusCommand)
	// Complete slash suggestions first, then execute the local command.
	m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if m.mode == ModeInsert {
		m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	}
	if m.mode != ModeStatus || len(m.queuedDrafts) != 0 {
		t.Fatal("status command was queued or sent instead of opening the overview")
	}
}
