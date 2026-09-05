package tui

import (
	"strings"
	"testing"
)

// TestErrorCardSharesTheCardShape pins the error card against its siblings: it
// was the only card with no conversation rail, it rendered its label as bare
// bold text instead of a badge, and it prefixed the first body line with "✗"
// while every other card leaves status signalling to the badge and the rail.
func TestErrorCardSharesTheCardShape(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:        1,
		Type:      BlockError,
		Content:   "stream interrupted: connection reset by peer",
		errorHint: "provider=anthropic status=502 retries=2",
	}
	lines := block.Render(90, "")
	plain := stripANSI(strings.Join(lines, "\n"))

	// Every card line but the trailing margin row carries the rail.
	rendered := stripANSILines(lines)
	if len(rendered) < 2 {
		t.Fatalf("expected a rendered card, got %d lines", len(rendered))
	}
	for i, line := range rendered[:len(rendered)-1] {
		if !strings.HasPrefix(line, "│") {
			t.Fatalf("expected every error card line to carry the rail, line %d = %q:\n%s", i, line, plain)
		}
	}
	if !strings.Contains(plain, "ERROR #2") {
		t.Fatalf("expected the ERROR badge, got:\n%s", plain)
	}
	if strings.Contains(plain, "✗") {
		t.Fatalf("expected no status glyph in the body, got:\n%s", plain)
	}
	if !strings.Contains(plain, "  stream interrupted: connection reset by peer") {
		t.Fatalf("expected the body at the shared two-space indent, got:\n%s", plain)
	}
	if !strings.Contains(plain, "  provider=anthropic status=502 retries=2") {
		t.Fatalf("expected the hint at the same indent, got:\n%s", plain)
	}
}

// TestStatusCardNamesAnUntitledNotice pins the badge fix: a runtime info event
// carries no title, and the card used to scavenge the first body line for one
// - which printed that line twice, and left a blank badge whenever the body
// was a single line.
func TestStatusCardNamesAnUntitledNotice(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockStatus, Content: "session exported to ~/chord-export.md"}
	plain := stripANSI(strings.Join(block.Render(90, ""), "\n"))
	if !strings.Contains(plain, infoCardTitle+" #2") {
		t.Fatalf("expected the %s badge, got:\n%s", infoCardTitle, plain)
	}

	multi := &Block{ID: 1, Type: BlockStatus, Content: "first line\nsecond line"}
	plain = stripANSI(strings.Join(multi.Render(90, ""), "\n"))
	if !strings.Contains(plain, infoCardTitle+" #2") {
		t.Fatalf("expected an untitled multi-line card to keep the %s badge, got:\n%s", infoCardTitle, plain)
	}
	if strings.Count(plain, "first line") != 1 {
		t.Fatalf("expected the first body line not to be duplicated as the badge, got:\n%s", plain)
	}
}
