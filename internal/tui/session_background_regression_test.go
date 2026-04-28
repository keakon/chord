package tui

import (
	"strings"
	"testing"
)

func TestAssistantPadBackgroundDoesNotStairStep(t *testing.T) {
	content := strings.Join([]string{
		"Regression guard for assistant card right-edge background padding.",
		"Force truncation with long inline code: `" + strings.Repeat("segment-", 30) + "\t" + strings.Repeat("X", 48) + "`.",
	}, "\n")
	b := &Block{Type: BlockAssistant, Content: content}
	width := 72

	lines := b.Render(width, "")
	cardBg := currentTheme.AssistantCardBg
	cardBgSeq := "\x1b[48;5;" + cardBg + "m"
	checked := 0

	for i, l := range lines {
		// Simulate viewport line safety pass.
		line := expandTabsForDisplayANSI(l, preformattedTabWidth)
		if strings.ContainsRune(line, '\t') {
			t.Fatalf("line %d still contains tab after expandTabsForDisplay: %q", i, line)
		}
		line = truncateLineToDisplayWidth(line, width)
		line = padLineToDisplayWidth(line, width)

		// Only enforce on card lines.
		if !strings.Contains(line, cardBgSeq) {
			continue
		}
		plain := strings.TrimSpace(stripANSI(line))
		if plain == "ASSISTANT" {
			continue
		}
		bg, ok := backgroundOnTrailingSpaces(line)
		if !ok {
			continue
		}
		checked++
		if bg != cardBg {
			t.Fatalf("line %d trailing pad bg=%q want %q\nplain=%q\nansi=%q", i, bg, cardBg, stripANSI(line), line)
		}
	}
	if checked == 0 {
		t.Fatalf("expected to check at least one assistant card line with trailing padding")
	}
}
