package tui

import (
	"strings"
	"testing"

	uv "github.com/keakon/ultraviolet"
)

// End-to-end against the real screen model: a card row holding a keycap emoji
// (1 + U+FE0F + U+20E3) must fill the card background exactly as far as every
// other row of the card. The screen parses drawn strings into cells; if its
// width method charged the cluster differently than the render pipeline that
// padded the line, the keycap row would end one column early and the
// terminal's default background would show at the card's right edge.
func TestKeycapCardRowFillsCardWidthOnScreen(t *testing.T) {
	const cardWidth = 60
	content := "not keycap → stripped. Good.What about \"1️⃣\" where FE0F is preceded by '1' and followed by U+20E3 — kept. But what if the FE0F is followed by U+20E3 but the"
	b := &Block{Type: BlockThinking, Content: content}
	lines := b.Render(cardWidth, "")

	termWidth := cardWidth + 6
	sb := newScreenBuffer(termWidth, len(lines))
	uv.NewStyledString(strings.Join(lines, "\n")).Draw(&sb, uv.Rect(0, 0, termWidth, len(lines)))

	// Reference card edge: the last styled column of the first (keycap-free)
	// row.
	reference := func(y int) int {
		row := sb.RenderBuffer.Line(y)
		last := -1
		for x := range termWidth {
			if c := row.At(x); c != nil && !c.Style.Bg.IsZero() {
				last = x
			}
		}
		return last
	}
	edge := reference(0)
	if edge < 0 {
		t.Fatal("reference row has no styled card background")
	}

	found := false
	for y := range lines {
		row := sb.RenderBuffer.Line(y)
		hasKeycap := false
		for x := range termWidth {
			if c := row.At(x); c != nil && strings.ContainsRune(c.Content, '\ufe0f') {
				hasKeycap = true
				break
			}
		}
		if !hasKeycap {
			continue
		}
		found = true
		if got := reference(y); got != edge {
			t.Fatalf("keycap row %d: card background ends at column %d, want %d (one column short exposes the default background)", y, got, edge)
		}
		if margin := row.At(edge + 1); margin != nil && !margin.Style.Bg.IsZero() {
			t.Fatalf("keycap row %d: surface background bleeds past the card edge", y)
		}
	}
	if !found {
		t.Fatal("no rendered row contains the keycap cluster")
	}
}
