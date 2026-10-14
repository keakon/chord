package terminaltext

import (
	"testing"

	"github.com/keakon/x/ansi"
)

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
			text := NormalizeGlyphs(tc.text)
			if got := Width(text); got != tc.want {
				t.Fatalf("Width(%q) = %d, want %d", text, got, tc.want)
			}
			if got := ansi.StringWidth(text); got != tc.want {
				t.Fatalf("ansi.StringWidth(%q) = %d, want %d", text, got, tc.want)
			}
			if tc.wantTerminalAdvance > 0 {
				if got := ansi.StringWidthWc(text); got != tc.wantTerminalAdvance {
					t.Fatalf("terminal advance of %q = %d, want %d (model width must match the terminal for standalone modifiers)", text, got, tc.wantTerminalAdvance)
				}
			}
			if again := NormalizeGlyphs(text); again != text {
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
			if got, want := Width(NormalizeGlyphs(tc.base+emojiModifier)), Width(tc.base); got != want {
				t.Fatalf("Width(%q) = %d, want %d (a joined modifier must keep the base width)", tc.base+emojiModifier, got, want)
			}
		})
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
		if got := RestoreModifiers(separateStandaloneEmojiModifiers(text)); got != text {
			t.Fatalf("round trip of %q = %q", text, got)
		}
	}
}
