package tui

import (
	"strings"
	"testing"

	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/tui/terminaltext"
)

// An emoji modifier (U+1F3FB–U+1F3FF) that follows a non-Emoji_Modifier_Base
// character is its own two-column glyph on terminals, but UAX #29 still merges
// it into the preceding grapheme cluster and the width libraries charge the
// cluster its leading character's width. Display normalization separates such
// a modifier into its own cluster; otherwise every following cell on the row
// paints two columns to the right: card surfaces stop short of the right edge
// and dialog borders/background drift by the same two columns. These tests pin
// the normalized cluster widths, the cell widths handed to the renderer, and
// the resulting card and dialog geometry.

const (
	emojiModifier     = "\U0001F3FF" // 🏿 emoji modifier Fitzpatrick type-6
	emojiModifierBase = "\U0001F44B" // 👋 waving hand, an Emoji_Modifier_Base
	detectiveRune     = "\U0001F575" // 🕵 sleuth, an Emoji_Modifier_Base without VS16
	thumbsUpRune      = "\U0001F44D" // 👍 thumbs up, an Emoji_Modifier_Base
)

func TestEmojiModifierCellKeepsFollowingColumnsAligned(t *testing.T) {
	const head = "warn a " + emojiModifier + " "
	tail := terminaltext.NormalizeGlyphs(head + "end")
	buf := newScreenBuffer(ansi.StringWidth(tail), 1)
	uv.NewStyledString(tail).Draw(buf, buf.Bounds())
	cells := buf.Line(0)

	// The terminal advances each code point on its own, so this is where it
	// paints the text after the modifier.
	start := ansi.StringWidthWc(head)
	if start+2 >= len(cells) {
		t.Fatalf("buffer too small: start=%d cells=%d", start, len(cells))
	}
	for i, want := range []string{"e", "n", "d"} {
		if got := cells[start+i].Content; got != want {
			t.Fatalf("cell %d content = %q, want %q (columns shifted)", start+i, got, want)
		}
	}
	var painted strings.Builder
	for _, c := range cells {
		painted.WriteString(c.Content)
	}
	if got, want := ansi.StringWidthWc(painted.String()), len(cells); got != want {
		t.Fatalf("terminal advance of painted cells = %d, want %d", got, want)
	}
}

// A card line carrying a standalone modifier must advance exactly as far on the
// terminal as the model claims, or the card surface ends short of the terminal's
// painted cells and the default background bleeds through at the right edge.
func TestCardLineTerminalAdvanceMatchesEmojiModifierWidth(t *testing.T) {
	const cardWidth = 100
	content := "the tool reported a standalone modifier: \"x " + emojiModifier + "\" done\n"

	b := &Block{Type: BlockAssistant, Content: content}
	rendered := b.Render(cardWidth, "")
	found := false
	for i, line := range rendered {
		if w := terminaltext.Width(line); w < cardWidth {
			t.Fatalf("line %d model width = %d, want >= %d (card background would not reach the right edge):\n%q", i, w, cardWidth, line)
		}
		if !strings.Contains(line, emojiModifier) {
			continue
		}
		found = true
		if advance := ansi.StringWidthWc(line); advance != terminaltext.Width(line) {
			t.Fatalf("line %d terminal advance = %d, model width = %d (terminal paints %d columns past the surface):\n%q",
				i, advance, terminaltext.Width(line), advance-terminaltext.Width(line), line)
		}
	}
	if !found {
		t.Fatalf("no rendered line contained the modifier payload:\n%s", strings.Join(rendered, "\n"))
	}
}

// End-to-end geometry: a confirmation dialog whose payload contains a
// standalone modifier must pad every line to the box width under the terminal's
// advance, so the right border stays in the same column on every row and the
// box neither overflows nor shifts.
func TestConfirmDialogGeometryWithEmojiModifier(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.confirm.request = &ConfirmRequest{
		ToolName: "shell",
		ArgsJSON: `{"command":"printf \"x ` + emojiModifier + `\"","workdir":"/tmp/project","timeout_ms":30000}`,
	}

	rendered := m.renderConfirmDialog()
	if rendered == "" {
		t.Fatal("expected a non-empty confirmation dialog")
	}
	if !strings.Contains(terminaltext.StripANSI(rendered), terminaltext.NormalizeGlyphs("x "+emojiModifier)) {
		t.Fatalf("dialog does not render the modifier payload:\n%s", terminaltext.StripANSI(rendered))
	}

	wantWidth := confirmDialogWidth(m.width)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		got := ansi.StringWidth(line)
		if got != wantWidth {
			t.Fatalf("line %d model width = %d, want %d:\n%q", i, got, wantWidth, terminaltext.StripANSI(line))
		}
		// The terminal advances each codepoint independently, so this is the
		// width the box will actually occupy when painted; a mismatch means the
		// right border lands outside the box on that row.
		if advance := ansi.StringWidthWc(line); advance != wantWidth {
			t.Fatalf("line %d terminal advance = %d, want %d (right border would shift):\n%q", i, advance, wantWidth, terminaltext.StripANSI(line))
		}
	}

	// The painted grid must keep the right border glyph in the last column of
	// every row: top-right corner, vertical border, bottom-right corner.
	for i, line := range lines {
		cells := drawLineCells(t, line)
		if len(cells) != wantWidth {
			t.Fatalf("line %d painted width = %d, want %d", i, len(cells), wantWidth)
		}
		want := "│"
		switch i {
		case 0:
			want = "╮"
		case len(lines) - 1:
			want = "╯"
		}
		if got := cells[wantWidth-1].Content; got != want {
			t.Fatalf("line %d last column = %q, want %q (border displaced):\n%q", i, got, want, terminaltext.StripANSI(line))
		}
	}
}
