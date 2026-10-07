package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/identity"
)

func TestHandoffDenyLayoutKeepsEditorAndActionsVisible(t *testing.T) {
	for _, size := range [][2]int{{30, 6}, {30, 12}, {40, 12}, {80, 16}, {80, 24}, {120, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := NewModelWithSize(&sessionControlAgent{availableAgents: []string{"builder"}}, 120, 40)
			m.openHandoffSelect("example.md", "request-1", identity.MainAgentID, m.mode)
			m.handoffSelect.planErr = ""
			m.handoffSelect.planText = strings.Repeat("Example plan line.\n", 30)
			m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Text: "r", Code: 'r'}))
			m.handoffSelect.denyReasonInput.SetValue("first line\nsecond line\nlast answer")
			m.handoffSelect.denyReasonInput.CursorEnd()
			m.applyTerminalSize(size[0], size[1], false)
			dialog := m.renderHandoffSelectDialog()
			if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
				t.Fatalf("dialog exceeds terminal bounds: %dx%d", lipgloss.Width(dialog), lipgloss.Height(dialog))
			}
			plain := strings.ToLower(ansi.Strip(dialog))
			for _, want := range []string{"last answer", "enter", "esc"} {
				if !strings.Contains(plain, want) {
					t.Fatalf("dialog lost %q:\n%s", want, plain)
				}
			}
			if !strings.Contains(ansi.Strip(m.View().Content), "last answer") {
				t.Fatal("answer is absent from the drawn frame")
			}
			m.handoffSelect.denyReasonInput.SetValue("")
			m.handleHandoffSelectKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
			dialog = m.renderHandoffSelectDialog()
			if lipgloss.Height(dialog) > size[1] || !strings.Contains(strings.ToLower(ansi.Strip(dialog)), "reason") {
				t.Fatalf("required-reason error is hidden or exceeds height:\n%s", ansi.Strip(dialog))
			}
		})
	}
}
