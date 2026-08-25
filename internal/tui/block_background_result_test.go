package tui

import (
	"strings"
	"testing"
)

func TestRenderBackgroundResultSanitizesControlSequences(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		Type:        BlockStatus,
		StatusTitle: backgroundResultCardTitle,
		Content:     "✓ job\nStatus: completed\nRelevant output:\nvalue \x1b[1;1H\x00\x9b31m",
	}
	raw := strings.Join(block.Render(100, ""), "\n")
	if strings.Contains(raw, "\x1b[1;1H") || strings.ContainsRune(raw, '\x00') || strings.ContainsRune(raw, '\x9b') {
		t.Fatalf("background result leaked raw control sequence: %q", raw)
	}
	plain := stripANSI(raw)
	for _, want := range []string{`\x1b[1;1H`, `\x00`, `\x9b31m`} {
		if !strings.Contains(plain, want) {
			t.Fatalf("background result missing escaped %s in %q", want, plain)
		}
	}
}
