package textclean

import (
	"testing"
)

func TestStripOrphanVariationSelectors(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		stripped int // number of selectors removed (for diagnostic clarity)
	}{
		{
			name:     "no selectors, empty string",
			input:    "",
			want:     "",
			stripped: 0,
		},
		{
			name:     "no selectors, plain ASCII",
			input:    "hello world",
			want:     "hello world",
			stripped: 0,
		},
		{
			name:     "legitimate emoji warning preserved",
			input:    "\u26a0\ufe0f warning",
			want:     "\u26a0\ufe0f warning",
			stripped: 0, // ⚠ is So (Other_Symbol), can take selector
		},
		{
			name:     "legitimate copyright emoji preserved",
			input:    "\u00a9\ufe0f 2024",
			want:     "\u00a9\ufe0f 2024",
			stripped: 0, // © is So
		},
		{
			name:     "orphan after space stripped",
			input:    "return \"\", \ufe0f0, false",
			want:     "return \"\", 0, false",
			stripped: 1,
		},
		{
			name:     "orphan after comma-space stripped",
			input:    "at lines \ufe0f12, \ufe0f45",
			want:     "at lines 12, 45",
			stripped: 2,
		},
		{
			name:     "orphan after letter stripped",
			input:    "a\ufe0fb",
			want:     "ab",
			stripped: 1, // 'a' is Ll, not So/Sk/Sm
		},
		{
			name:     "orphan at start of string stripped",
			input:    "\ufe0f0",
			want:     "0",
			stripped: 1, // prev is rune(-1)
		},
		{
			name:     "mixed legitimate and orphan",
			input:    "\u26a0\ufe0f value \ufe0f42 done \u2705\ufe0f",
			want:     "\u26a0\ufe0f value 42 done \u2705\ufe0f",
			stripped: 1, // only the one before '42' is orphan (space predecessor); ✅ is legitimate
		},
		{
			name:     "FE0E text selector also handled",
			input:    "count\ufe0ee: 5", // U+FE0E followed by 'e' — orphan because 'e' is Ll
			want:     "counte: 5",
			stripped: 1,
		},
		{
			name:     "consecutive orphans all stripped",
			input:    "a\ufe0f\ufe0f\ufe0fb",
			want:     "ab",
			stripped: 3,
		},
		{
			name:     "arrow symbol (Sm) keeps selector",
			input:    "\u2190\ufe0f next",
			want:     "\u2190\ufe0f next",
			stripped: 0, // ← is Sm (Math_Symbol)
		},
		{
			name:     "keycap emoji kept with selector",
			input:    "press 1\ufe0f\u20e3 to start",
			want:     "press 1\ufe0f\u20e3 to start",
			stripped: 0, // full keycap sequence: digit + FE0F + U+20E3
		},
		{
			name:     "defective keycap without enclosing keycap stripped",
			input:    "slot 1\ufe0f empty",
			want:     "slot 1 empty",
			stripped: 1, // digit + FE0F but no trailing U+20E3
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StripOrphanVariationSelectors(tc.input)
			if got != tc.want {
				t.Errorf("StripOrphanVariationSelectors(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCanTakeVariationSelector(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{'\u26a0', true}, // So (warning sign)
		{'\u00a9', true}, // So (copyright)
		{'\u2122', true}, // So (trade mark)
		{'\u2190', true}, // Sm (leftwards arrow)
		{'\u2192', true}, // Sm (rightwards arrow)
		{'a', false},     // Ll (lowercase letter)
		{'Z', false},     // Lu (uppercase letter)
		{' ', false},     // Zs (space separator)
		{'0', false},     // Nd (decimal number; only a full keycap sequence is valid)
		{'^', false},     // Sk, but ASCII: Unicode gives no variation sequence for it
		{'`', false},     // Sk, but ASCII: Unicode gives no variation sequence for it
		{'-', false},     // Pd (dash punctuation)
	}
	for _, tc := range tests {
		t.Run(string(tc.r), func(t *testing.T) {
			if got := canTakeVariationSelector(tc.r); got != tc.want {
				t.Errorf("canTakeVariationSelector(%U) = %v; want %v", tc.r, got, tc.want)
			}
		})
	}
}

func TestStripZeroWidthFormat(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"plain ascii untouched", "hello world", "hello world"},
		{"zwsp stripped", "a\u200bb", "ab"},
		{"zwnj stripped", "a\u200cb", "ab"},
		{"zwj stripped", "a\u200db", "ab"},
		{"word joiner stripped", "a\u2060b", "ab"},
		{"bom mid-stream stripped", "ab\ufeffcd", "abcd"},
		{"variation selector fe00 stripped", "a\ufe00b", "ab"},
		{"variation selector fe0d stripped", "a\ufe0db", "ab"},
		{"soft hyphen stripped", "ab\u00adcd", "abcd"},
		{"mixed zero-width stripped", "a\u200b\u200d\u200cb", "ab"},
		{"narrow no-break space preserved", "a\u202fb", "a\u202fb"},
		{"no-break space preserved", "a\u00a0b", "a\u00a0b"},
		{"em space preserved", "a\u2003b", "a\u2003b"},
		{"zwj emoji sequence preserved", "\U0001f468\u200d\U0001f469\u200d\U0001f467", "\U0001f468\u200d\U0001f469\u200d\U0001f467"},
		{"zwj after variation selector preserved", "\U0001f3f3\ufe0f\u200d\U0001f308", "\U0001f3f3\ufe0f\u200d\U0001f308"},
		{"zwj after text-style selector preserved", "\u2764\ufe0f\u200d\U0001f525", "\u2764\ufe0f\u200d\U0001f525"},
		{"zwj after eye selector preserved", "\U0001f441\ufe0f\u200d\U0001f5e8\ufe0f", "\U0001f441\ufe0f\u200d\U0001f5e8\ufe0f"},
		{"zwj after skin tone preserved", "\U0001f44d\U0001f3fb\u200d\U0001f527", "\U0001f44d\U0001f3fb\u200d\U0001f527"},
		{"zwj inside sequence keeps later selectors", "\U0001f441\ufe0f\u200d\U0001f5e8\ufe0f\u200d\U0001f469", "\U0001f441\ufe0f\u200d\U0001f5e8\ufe0f\u200d\U0001f469"},
		{"zwj between emoji and non-emoji stripped", "a\u200d\U0001f469", "a\U0001f469"},
		{"zwj between non-emoji and emoji stripped", "\U0001f469\u200db", "\U0001f469b"},
		{"leading zwj stripped", "\u200d\U0001f469", "\U0001f469"},
		{"trailing zwj stripped", "\U0001f469\u200d", "\U0001f469"},
		{"zwj after selector without trailing base stripped", "\U0001f3f3\ufe0f\u200dx", "\U0001f3f3\ufe0fx"},
		{"zwsp inside emoji sequence stripped", "\U0001f468\u200b\u200d\U0001f469", "\U0001f468\u200d\U0001f469"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripZeroWidthFormat(tc.input); got != tc.want {
				t.Fatalf("StripZeroWidthFormat(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestStripZeroWidthFormatKeepsLeadingBOM guards that a leading BOM survives
// as byte-order encoding (readFileForEdit detects it) rather than being
// mistaken for model corruption.
func TestStripZeroWidthFormatKeepsLeadingBOM(t *testing.T) {
	if got := StripZeroWidthFormat("\ufeffhello"); got != "\ufeffhello" {
		t.Fatalf("StripZeroWidthFormat(leading BOM) = %q, want it preserved", got)
	}
}
