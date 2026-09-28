package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	uv "github.com/keakon/ultraviolet"
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

func TestEmojiModifierClusterWidths(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
		// wantTerminalAdvance is the per-codepoint advance a terminal charges
		// for the rune sequence; it must match the model width exactly where a
		// standalone modifier makes the merged cluster wider than its leading
		// character. Zero means the two measures legitimately differ (merged
		// base+modifier or VS16 presentation) and is not compared.
		wantTerminalAdvance int
	}{
		{"modifier_after_space", " " + emojiModifier, 3, 3},
		{"modifier_after_ascii", "a" + emojiModifier, 3, 3},
		{"lone_modifier", emojiModifier, 2, 2},
		{"consecutive_modifiers", emojiModifier + emojiModifier, 4, 4},
		{"modifier_after_emoji_base", emojiModifierBase + emojiModifier, 2, 0},
		{"modifier_after_base_without_selector", detectiveRune + emojiModifier, 1, 0},
		{"thumbs_up_with_tone", thumbsUpRune + emojiModifier, 2, 0},
		{"emoji_base_alone", emojiModifierBase, 2, 0},
		{"selector_emoji_alone", "\u26A0\uFE0F", 2, 0},
		{"base_without_selector_alone", detectiveRune, 1, 0},
		{"selector_between_base_and_modifier", detectiveRune + "\ufe0f" + emojiModifier, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := normalizeDisplayGlyphs(tc.text)
			if got := tuiStringWidth(text); got != tc.want {
				t.Fatalf("tuiStringWidth(%q) = %d, want %d", text, got, tc.want)
			}
			if got := ansi.StringWidth(text); got != tc.want {
				t.Fatalf("ansi.StringWidth(%q) = %d, want %d", text, got, tc.want)
			}
			if tc.wantTerminalAdvance > 0 {
				if got := ansi.StringWidthWc(text); got != tc.wantTerminalAdvance {
					t.Fatalf("terminal advance of %q = %d, want %d (model width must match the terminal for standalone modifiers)", text, got, tc.wantTerminalAdvance)
				}
			}
			if again := normalizeDisplayGlyphs(text); again != text {
				t.Fatalf("normalization is not idempotent: %q -> %q", text, again)
			}
		})
	}
}

func TestSeparateStandaloneEmojiModifiersLeavesJoinedSequencesAlone(t *testing.T) {
	for _, text := range []string{
		"plain ascii",
		emojiModifier + " leading",
		emojiModifierBase + emojiModifier + " wave",
		"\U0001F3C0 basketball shares the modifier prefix bytes",
		"line\n" + emojiModifier,
	} {
		if got := separateStandaloneEmojiModifiers(text); got != text {
			t.Fatalf("separateStandaloneEmojiModifiers(%q) = %q, want unchanged", text, got)
		}
	}
}

// A modifier that joins an Emoji_Modifier_Base advances as the base does, so
// the sequence must keep the base's width — including text-presentation bases:
// Ghostty charges U+1F575 plus a modifier one column, like the bare base.
// Once charged two, every row carrying 🕵🏻 came out one column short of the
// card surface (confirmed against a recorded screenshot).
func TestEmojiModifierAfterBaseAdvancesLikeBase(t *testing.T) {
	cases := []struct {
		name string
		base string
	}{
		{"text_presentation_base", detectiveRune},
		{"emoji_presentation_base", emojiModifierBase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := tuiStringWidth(normalizeDisplayGlyphs(tc.base+emojiModifier)), tuiStringWidth(tc.base); got != want {
				t.Fatalf("tuiStringWidth(%q) = %d, want %d (a joined modifier must keep the base width)", tc.base+emojiModifier, got, want)
			}
		})
	}
}

func TestEmojiModifierCellKeepsFollowingColumnsAligned(t *testing.T) {
	const head = "warn a " + emojiModifier + " "
	tail := normalizeDisplayGlyphs(head + "end")
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
		if w := tuiStringWidth(line); w < cardWidth {
			t.Fatalf("line %d model width = %d, want >= %d (card background would not reach the right edge):\n%q", i, w, cardWidth, line)
		}
		if !strings.Contains(line, emojiModifier) {
			continue
		}
		found = true
		if advance := ansi.StringWidthWc(line); advance != tuiStringWidth(line) {
			t.Fatalf("line %d terminal advance = %d, model width = %d (terminal paints %d columns past the surface):\n%q",
				i, advance, tuiStringWidth(line), advance-tuiStringWidth(line), line)
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
	if !strings.Contains(stripANSI(rendered), normalizeDisplayGlyphs("x "+emojiModifier)) {
		t.Fatalf("dialog does not render the modifier payload:\n%s", stripANSI(rendered))
	}

	wantWidth := confirmDialogWidth(m.width)
	lines := strings.Split(rendered, "\n")
	for i, line := range lines {
		got := ansi.StringWidth(line)
		if got != wantWidth {
			t.Fatalf("line %d model width = %d, want %d:\n%q", i, got, wantWidth, stripANSI(line))
		}
		// The terminal advances each codepoint independently, so this is the
		// width the box will actually occupy when painted; a mismatch means the
		// right border lands outside the box on that row.
		if advance := ansi.StringWidthWc(line); advance != wantWidth {
			t.Fatalf("line %d terminal advance = %d, want %d (right border would shift):\n%q", i, advance, wantWidth, stripANSI(line))
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
			t.Fatalf("line %d last column = %q, want %q (border displaced):\n%q", i, got, want, stripANSI(line))
		}
	}
}

func TestJoinStandaloneEmojiModifiersRestoresOriginal(t *testing.T) {
	for _, text := range []string{
		"x " + emojiModifier + " done",
		emojiModifier + emojiModifier,
		"a" + emojiModifier + "b" + emojiModifier,
		emojiModifierBase + emojiModifier,
		"\u200b\U0001F3C0 keeps a break before a non-modifier",
	} {
		if got := joinStandaloneEmojiModifiers(separateStandaloneEmojiModifiers(text)); got != text {
			t.Fatalf("round trip of %q = %q", text, got)
		}
	}
}
