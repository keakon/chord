package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"
	"github.com/keakon/lipgloss/v2"
)

func TestPoolFilterSmallTerminalKeepsSelectionAndMouseTargetVisible(t *testing.T) {
	for _, size := range [][2]int{{30, 6}, {40, 12}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m, backend := newPoolSwitchModel()
			backend.mainModelPoolNames = []string{"alpha", "beta"}
			backend.mainModelPool = "alpha"
			m.applyTerminalSize(size[0], size[1], false)
			m.openModelSelect()
			m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
			m.Update(tea.PasteMsg{Content: "beta"})
			m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			dialog := m.renderModelSelectDialog()
			plain := stripANSI(dialog)
			if lipgloss.Height(dialog) > size[1] || lipgloss.Width(dialog) > size[0]-1 {
				t.Fatal("model selector exceeds terminal bounds")
			}
			if !strings.Contains(plain, "filter: beta") || !strings.Contains(strings.ToLower(plain), "enter") || !strings.Contains(strings.ToLower(plain), "esc") {
				t.Fatalf("filter or actions are hidden: %s", plain)
			}
			rect := m.overlayRect(dialog)
			row := rect.Min.Y + 1 + m.modelSelect.selector.listBaseRow
			lines := strings.Split(plain, "\n")
			if localRow := row - rect.Min.Y; localRow >= len(lines) || !strings.Contains(lines[localRow], "beta") {
				t.Fatalf("mouse target does not match the visible result: %s", plain)
			}
			idx, ok := m.poolSelectIndexAt(rect.Min.X+2, row)
			if !ok || idx != 0 {
				t.Fatalf("mouse selection = %d, %t", idx, ok)
			}
		})
	}
}

func TestPoolFilterSelectsVisibleIdentityAndKeepsNavigationLetters(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"alpha", "jkg pool", "other"}
	backend.mainModelPool = "alpha"
	m.openModelSelect()
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	for _, r := range "jkg" {
		m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	if m.modelSelect.filter != "jkg" || m.modelSelect.selector.list.Len() != 1 {
		t.Fatalf("filter = %q", m.modelSelect.filter)
	}
	m.renderModelSelectDialog()
	runCmdTree(m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if strings.Join(backend.setCurrentModelPoolCalls, ",") != "jkg pool" {
		t.Fatalf("selected wrong pool: %v", backend.setCurrentModelPoolCalls)
	}
}

func TestPoolFilterEmptyResultsCannotSwitchAndEscClears(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"alpha", "beta"}
	backend.mainModelPool = "alpha"
	m.openModelSelect()
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: 'z', Text: "z"}))
	if !strings.Contains(stripANSI(m.renderModelSelectDialog()), "No matching pools") {
		t.Fatal("missing empty state")
	}
	runCmdTree(m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if m.mode != ModeModelSelect || len(backend.setCurrentModelPoolCalls) != 0 {
		t.Fatal("empty result submitted")
	}
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.modelSelect.filter != "" || m.modelSelect.selector.list.Len() != 2 || m.mode != ModeModelSelect {
		t.Fatal("escape must clear filter before closing")
	}
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if m.mode != ModeNormal {
		t.Fatal("escape did not restore mode")
	}
}

func TestPoolFilterAcceptsTerminalPasteOnlyWhileEditing(t *testing.T) {
	m, backend := newPoolSwitchModel()
	backend.mainModelPoolNames = []string{"alpha", "jkg pool", "other"}
	backend.mainModelPool = "alpha"
	m.openModelSelect()
	m.Update(tea.PasteMsg{Content: "alpha"})
	if m.modelSelect.filter != "" {
		t.Fatal("paste outside filter editing changed the query")
	}
	m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: '/', Text: "/"}))
	m.Update(tea.PasteMsg{Content: "jkg\npool"})
	if m.modelSelect.filter != "jkg pool" || m.modelSelect.selector.list.Len() != 1 {
		t.Fatalf("pasted filter = %q", m.modelSelect.filter)
	}
	runCmdTree(m.handleModelSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})))
	if strings.Join(backend.setCurrentModelPoolCalls, ",") != "jkg pool" {
		t.Fatalf("paste selected wrong pool: %v", backend.setCurrentModelPoolCalls)
	}
}
