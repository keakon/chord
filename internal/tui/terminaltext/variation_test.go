package terminaltext

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/textclean"
)

func TestStripOrphanVariationSelectors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Legitimate emoji presentation must survive.
		{"warning_emoji_kept", "⚠️ 部分", "⚠️ 部分"},
		{"copyright_emoji_kept", "©️ 2026", "©️ 2026"},

		// Keycap sequences: the base is ASCII, so only the trailing U+20E3
		// distinguishes the emoji from a defective sequence.
		{"keycap_digit_kept", "1️⃣ 先跑测试", "1️⃣ 先跑测试"},
		{"keycap_zero_kept", "0️⃣", "0️⃣"},
		{"keycap_hash_kept", "#️⃣", "#️⃣"},
		{"keycap_star_kept", "*️⃣", "*️⃣"},

		// Non-symbol bases Unicode still gives an emoji variation sequence.
		{"double_exclamation_kept", "‼️", "‼️"},
		{"exclamation_question_kept", "⁉️", "⁉️"},
		{"information_kept", "ℹ️ 提示", "ℹ️ 提示"},
		{"wavy_dash_kept", "〰️", "〰️"},
		{"part_alternation_kept", "〽️", "〽️"},

		// Sm bases: the arrow emoji are split between So and Sm, so both
		// categories must be allowed.
		{"left_right_arrow_kept", "↔️", "↔️"},
		{"up_down_arrow_kept", "↕️", "↕️"},
		{"curving_arrow_kept", "⤴️", "⤴️"},

		// Orphans: base emoji dropped, only the selector remains.
		{"after_space", "+ ️1", "+ 1"},
		{"after_ascii_letter", "a️", "a"},
		{"after_digit", "2️", "2"},
		{"after_digit_no_keycap", "1️x", "1x"},
		{"after_ascii_math_symbol", "+️x", "+x"},
		{"after_equals", "=️", "="},
		{"after_cjk", "中️", "中"},
		{"after_quote", "'️'", "''"},
		{"after_fullwidth_punct", "。️", "。"},
		{"at_start", "️abc", "abc"},
		{"consecutive_orphans", " ️️️", " "},
		{"table_cell_orphan", "| ️6（兜底）|", "| 6（兜底）|"},

		// Mixed: keep the real emoji, drop the orphan.
		{"mixed", "⚠️ 注意 + ️1 项", "⚠️ 注意 + 1 项"},
		{"keycap_next_to_orphan", "1️⃣ 与 ️2", "1️⃣ 与 2"},

		// Variants.
		{"text_selector_orphan", "a︎", "a"},
		{"empty", "", ""},
		{"no_selector", "plain text", "plain text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := textclean.StripOrphanVariationSelectors(tc.in)
			if got != tc.want {
				t.Fatalf("StripOrphanVariationSelectors(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The width method must not charge a column for a stripped orphan.
func TestOrphanSelectorWidthAfterStrip(t *testing.T) {
	// Width calculation also rejects VS16 on an ineligible base. Normalizing
	// display text must preserve that width rather than change the layout.
	if got := Width(" \ufe0f"); got != 1 {
		t.Fatalf("Width(%q) = %d, want 1", " \ufe0f", got)
	}
	stripped := textclean.StripOrphanVariationSelectors(" ️")
	if got := Width(stripped); got != 1 {
		t.Fatalf("width after strip = %d, want 1", got)
	}
}

// Stripping a legitimate selector costs a column in the opposite direction: the
// terminal keeps painting a keycap two cells wide while the width method drops
// to one, so the line overflows the card instead of under-filling it. Pin that
// keycap sequences survive with their measured width intact.
func TestKeycapSequenceKeepsMeasuredWidth(t *testing.T) {
	const keycap = "1️⃣"
	before := Width(keycap)
	if got := textclean.StripOrphanVariationSelectors(keycap); got != keycap {
		t.Fatalf("keycap emoji was mangled: %q -> %q", keycap, got)
	}
	if after := Width(textclean.StripOrphanVariationSelectors(keycap)); after != before {
		t.Fatalf("keycap width changed: %d -> %d (a narrower measurement overflows the card)", before, after)
	}
	if before != 2 {
		t.Fatalf("Width(%q) = %d, want 2", keycap, before)
	}
}

// Sanitize is the shared entry point, so orphans must be removed
// even when the text has no control characters to sanitize.
func TestSanitizeDisplayTextStripsOrphans(t *testing.T) {
	in := "结果 + ️1 大于 ️2"
	got := Sanitize(in)
	if strings.ContainsRune(got, '\ufe0f') {
		t.Fatalf("Sanitize left an orphan selector: %q", got)
	}
	if want := "结果 + 1 大于 2"; got != want {
		t.Fatalf("Sanitize(%q) = %q, want %q", in, got, want)
	}
}

// The single detection scan ends early at a control character, so an orphan
// hiding after one is only covered by the needsSanitization branch of the
// strip condition.
func TestSanitizeDisplayTextStripsOrphanAfterControl(t *testing.T) {
	in := "\x00 rest \ufe0f1"
	got := Sanitize(in)
	if strings.ContainsRune(got, '\ufe0f') {
		t.Fatalf("orphan after a control character survived: %q", got)
	}
}
