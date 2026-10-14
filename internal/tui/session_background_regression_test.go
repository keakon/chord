package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestAssistantPadBackgroundDoesNotStairStep(t *testing.T) {
	content := strings.Join([]string{
		"Regression guard for assistant card right-edge background padding.",
		"Force truncation with long inline code: `" + strings.Repeat("segment-", 30) + "\t" + strings.Repeat("X", 48) + "`.",
	}, "\n")
	b := &Block{Type: BlockAssistant, Content: content}
	width := 72

	lines := b.Render(width, "")
	cardBg := currentStyles.Palette.AssistantCardBg
	cardBgSeq := "\x1b[48;5;" + cardBg + "m"
	checked := 0

	for i, l := range lines {
		// Simulate viewport line safety pass.
		line := terminaltext.ExpandTabsANSI(l, terminaltext.TabWidth)
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("line %d still contains tab after terminaltext.ExpandTabs: %q", i, line)
		}
		line = terminaltext.Truncate(line, width)
		line = terminaltext.Pad(line, width)

		// Only enforce on card lines.
		if !strings.Contains(line, cardBgSeq) {
			continue
		}
		plain := strings.TrimSpace(terminaltext.StripANSI(line))
		if plain == "ASSISTANT" {
			continue
		}
		bg, ok := backgroundOnTrailingSpaces(line)
		if !ok {
			continue
		}
		checked++
		if bg != cardBg {
			t.Fatalf("line %d trailing pad bg=%q want %q\nplain=%q\nansi=%q", i, bg, cardBg, terminaltext.StripANSI(line), line)
		}
	}
	if checked == 0 {
		t.Fatalf("expected to check at least one assistant card line with trailing padding")
	}
}
