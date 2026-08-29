package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// Orphaned variation selectors are invisible but not harmless: the width
// method charges them a column the terminal never paints, which shortens card
// background padding and lets the terminal default bleed in at the right edge.
// These tests pin both the stripping rule and the layout consequence.

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
			got := tools.StripOrphanVariationSelectors(tc.in)
			if got != tc.want {
				t.Fatalf("StripOrphanVariationSelectors(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The width method must not charge a column for a stripped orphan.
func TestOrphanSelectorWidthAfterStrip(t *testing.T) {
	// Premise of the whole strip fix: the width library charges an orphaned
	// selector one column the terminal never paints. If that ever changes,
	// the strip's rationale must be re-examined.
	if got := tuiStringWidth(" ️"); got != 2 {
		t.Fatalf("tuiStringWidth(%q) = %d, want 2 (width library no longer charges orphans a column; revisit the strip premise)", " ️", got)
	}
	stripped := tools.StripOrphanVariationSelectors(" ️")
	if got := tuiStringWidth(stripped); got != 1 {
		t.Fatalf("width after strip = %d, want 1", got)
	}
}

// Stripping a legitimate selector costs a column in the opposite direction: the
// terminal keeps painting a keycap two cells wide while the width method drops
// to one, so the line overflows the card instead of under-filling it. Pin that
// keycap sequences survive with their measured width intact.
func TestKeycapSequenceKeepsMeasuredWidth(t *testing.T) {
	const keycap = "1️⃣"
	before := tuiStringWidth(keycap)
	if got := tools.StripOrphanVariationSelectors(keycap); got != keycap {
		t.Fatalf("keycap emoji was mangled: %q -> %q", keycap, got)
	}
	if after := tuiStringWidth(tools.StripOrphanVariationSelectors(keycap)); after != before {
		t.Fatalf("keycap width changed: %d -> %d (a narrower measurement overflows the card)", before, after)
	}
	if before != 2 {
		t.Fatalf("tuiStringWidth(%q) = %d, want 2", keycap, before)
	}
}

// sanitizeDisplayText is the shared entry point, so orphans must be removed
// even when the text has no control characters to sanitize.
func TestSanitizeDisplayTextStripsOrphans(t *testing.T) {
	in := "结果 + ️1 大于 ️2"
	got := sanitizeDisplayText(in)
	if strings.ContainsRune(got, '\ufe0f') {
		t.Fatalf("sanitizeDisplayText left an orphan selector: %q", got)
	}
	if want := "结果 + 1 大于 2"; got != want {
		t.Fatalf("sanitizeDisplayText(%q) = %q, want %q", in, got, want)
	}
}

// The single detection scan ends early at a control character, so an orphan
// hiding after one is only covered by the needsSanitization branch of the
// strip condition.
func TestSanitizeDisplayTextStripsOrphanAfterControl(t *testing.T) {
	in := "\x00 rest \ufe0f1"
	got := sanitizeDisplayText(in)
	if strings.ContainsRune(got, '\ufe0f') {
		t.Fatalf("orphan after a control character survived: %q", got)
	}
}

// End-to-end: a card built from orphan-bearing content must paint its surface
// across the full width, with no terminal-default columns at the right edge.
// Before stripping, each orphan left the card one column short.
func TestCardBackgroundFillsWidthWithOrphans(t *testing.T) {
	const cardWidth = 100
	// Realistic content: prose with orphaned selectors and a markdown table.
	content := "验证结果：differing line ️1 与 line ️2 均已入列，\n\n" +
		"| 层级 | 条件 |\n|---|---|\n| 1 | line ️1 命中 |\n| 2 | line ️2 兜底 |\n"

	b := &Block{Type: BlockAssistant, Content: content}
	lines := b.Render(cardWidth, "")

	for i, l := range lines {
		n := strings.Count(l, "\ufe0f")
		if n != 0 {
			t.Fatalf("line %d still contains %d orphan selector(s) in rendered output: %q",
				i, n, l)
		}
		expanded := expandTabsForDisplayANSI(l, preformattedTabWidth)
		if w := tuiStringWidth(expanded); w < cardWidth {
			t.Fatalf("line %d width = %d, want >= %d (card background would not reach the right edge):\n%q",
				i, w, cardWidth, l)
		}
	}
}

// End-to-end: the edit error card renders old_string/new_string through the
// code-highlighted preview, which bypasses sanitizeDisplayText. Args carrying
// orphaned selectors must render with no selector left and a full-width
// background. This is the shape observed in the failing session: an old_string
// copied with " ️12,  ️45" where the file had "12, 45".
func TestEditErrorCardPreviewStripsOrphans(t *testing.T) {
	const cardWidth = 100
	args, err := json.Marshal(map[string]string{
		"path":       "src/sample.go",
		"old_string": "// messages: \"at lines  \ufe0f12,  \ufe0f45\" for many\nfunc old() {}\n",
		"new_string": "// replacement\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       string(args),
		RawArgs:       string(args),
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "Error: old_string not found in file, even after punctuation/whitespace tolerance.",
	}

	for i, l := range b.Render(cardWidth, "") {
		if n := strings.Count(l, "\ufe0f") + strings.Count(l, "\ufe0e"); n != 0 {
			t.Fatalf("line %d still contains %d orphan selector(s): %q", i, n, l)
		}
		expanded := expandTabsForDisplayANSI(l, preformattedTabWidth)
		if w := tuiStringWidth(expanded); w < cardWidth {
			t.Fatalf("line %d width = %d, want >= %d (card background would not reach the right edge):\n%q",
				i, w, cardWidth, l)
		}
	}
}

// End-to-end: the apply_patch error card shows the requested patch through the
// same highlighted preview path, so orphan-bearing patch text must be stripped
// before rendering too.
func TestApplyPatchErrorCardPreviewStripsOrphans(t *testing.T) {
	const cardWidth = 100
	patch := "*** Begin Patch\n*** Update File: src/sample.go\n@@\n-count is \ufe0f42\n+count is 42\n"
	args, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	b := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(string(args)),
		RawArgs:       string(args),
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "Error: hunk failed to apply",
	}

	for i, l := range b.Render(cardWidth, "") {
		if n := strings.Count(l, "\ufe0f") + strings.Count(l, "\ufe0e"); n != 0 {
			t.Fatalf("line %d still contains %d orphan selector(s): %q", i, n, l)
		}
		expanded := expandTabsForDisplayANSI(l, preformattedTabWidth)
		if w := tuiStringWidth(expanded); w < cardWidth {
			t.Fatalf("line %d width = %d, want >= %d (card background would not reach the right edge):\n%q",
				i, w, cardWidth, l)
		}
	}
}

// The success-state diff renders file contents, which can legitimately carry
// orphaned selectors left by editors or earlier tool runs. Both the one-sided
// and paired diff line renderers must strip them before width math.
func TestUnifiedDiffLineStripsOrphanSelectors(t *testing.T) {
	hl := newCodeHighlighterWithLanguage("src/sample.go", "count is 42", "")
	var out []string
	appendApplyPatchToolUnifiedDiffLine(&out, "count is \ufe0f42", 1, 60, hl, false)
	appendApplyPatchToolUnifiedDiffPair(&out, "old value \ufe0f1", "new value 1", 1, 2, 60, hl)
	joined := strings.Join(out, "\n")
	if strings.ContainsRune(joined, '\ufe0f') || strings.ContainsRune(joined, '\ufe0e') {
		t.Fatalf("diff line renderers left an orphan selector: %q", joined)
	}
}
