package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
)

func TestScrollablePanelsFitSmallTerminalsAndReachLastLine(t *testing.T) {
	for _, size := range [][2]int{{30, 12}, {40, 12}, {80, 24}, {120, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m := NewModelWithSize(&sessionControlAgent{}, size[0], size[1])
			m.openUsageStats()
			for _, dialog := range []string{m.renderUsageStatsDialog(), m.renderErrorPanelDialog()} {
				if lipgloss.Width(dialog) > size[0]-1 || lipgloss.Height(dialog) > size[1] {
					t.Fatalf("panel %dx%d exceeds terminal bounds", lipgloss.Width(dialog), lipgloss.Height(dialog))
				}
			}
			m.handleUsageStatsKey(tea.KeyPressMsg(tea.Key{Text: "G", Code: 'G'}))
			lines := m.usageStatsLines(m.usageStatsInnerWidth())
			last := strings.TrimSpace(ansi.Strip(lines[len(lines)-1]))
			if last != "" && !strings.Contains(ansi.Strip(m.renderUsageStatsDialog()), last) {
				t.Fatalf("stats last line is not reachable: last=%q total=%d visible=%d offset=%d\n%s", last, len(lines), m.usageStatsVisibleLines(), m.usageStats.scrollOffset, ansi.Strip(m.renderUsageStatsDialog()))
			}
		})
	}
}

func TestHelpIntroductionWrapsWithoutLosingInteractionInstructions(t *testing.T) {
	m := NewModel(nil)
	m.keyMap.InsertSubmit = []string{"ctrl+s"}
	for _, width := range []int{30, 40, 80, 120} {
		lines := m.helpLines(width)
		for _, line := range lines {
			if ansi.StringWidth(line) > width {
				t.Fatalf("help line exceeds %d columns: %q", width, ansi.Strip(line))
			}
		}
		text := strings.Join(strings.Fields(ansi.Strip(strings.Join(lines, "\n"))), " ")
		for _, want := range []string{"ctrl+s completes", "Esc stops the current run", "without resuming the run", "[del]"} {
			if !strings.Contains(text, want) {
				t.Fatalf("help at width %d lost %q", width, want)
			}
		}
	}
}
