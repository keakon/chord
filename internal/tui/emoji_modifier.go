package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/keakon/chord/internal/tools"
)

// Emoji modifiers (U+1F3FB–U+1F3FF) are part of the preceding character only
// when that character is an Emoji_Modifier_Base: UTS #51 defines an emoji
// modifier sequence as Emoji_Modifier_Base followed by Emoji_Modifier. A
// modifier anywhere else is a standalone two-column glyph (the color swatch it
// renders as), and terminals advance it that way.
//
// UAX #29 instead treats a modifier as Extend, so it joins whatever precedes
// it, and the width libraries measure a cluster by its leading character:
// " 🏿" measures one column while the terminal advances three, shifting every
// following cell on the row. Rendered text therefore gets a zero-width space
// (a grapheme cluster break) in front of each standalone modifier, which makes
// every width measurement see the modifier as its own two-column cluster.

const (
	emojiModifierFirst = '\U0001F3FB'
	emojiModifierLast  = '\U0001F3FF'
	// emojiModifierPrefix is the UTF-8 encoding shared by every emoji
	// modifier; only the fourth byte (0xBB–0xBF) varies.
	emojiModifierPrefix = "\xF0\x9F\x8F"
	zeroWidthSpace      = '\u200b'
	variationSelector16 = '\ufe0f'
)

// emojiModifierBaseRanges holds the Emoji_Modifier_Base code points of Unicode
// 17.0.0 (emoji-data.txt) as sorted, non-overlapping [lo, hi] pairs.
var emojiModifierBaseRanges = [...][2]rune{
	{0x261D, 0x261D},
	{0x26F9, 0x26F9},
	{0x270A, 0x270D},
	{0x1F385, 0x1F385},
	{0x1F3C2, 0x1F3C4},
	{0x1F3C7, 0x1F3C7},
	{0x1F3CA, 0x1F3CC},
	{0x1F442, 0x1F443},
	{0x1F446, 0x1F450},
	{0x1F466, 0x1F478},
	{0x1F47C, 0x1F47C},
	{0x1F481, 0x1F483},
	{0x1F485, 0x1F487},
	{0x1F48F, 0x1F48F},
	{0x1F491, 0x1F491},
	{0x1F4AA, 0x1F4AA},
	{0x1F574, 0x1F575},
	{0x1F57A, 0x1F57A},
	{0x1F590, 0x1F590},
	{0x1F595, 0x1F596},
	{0x1F645, 0x1F647},
	{0x1F64B, 0x1F64F},
	{0x1F6A3, 0x1F6A3},
	{0x1F6B4, 0x1F6B6},
	{0x1F6C0, 0x1F6C0},
	{0x1F6CC, 0x1F6CC},
	{0x1F90C, 0x1F90C},
	{0x1F90F, 0x1F90F},
	{0x1F918, 0x1F91F},
	{0x1F926, 0x1F926},
	{0x1F930, 0x1F939},
	{0x1F93C, 0x1F93E},
	{0x1F977, 0x1F977},
	{0x1F9B5, 0x1F9B6},
	{0x1F9B8, 0x1F9B9},
	{0x1F9BB, 0x1F9BB},
	{0x1F9CD, 0x1F9CF},
	{0x1F9D1, 0x1F9DD},
	{0x1FAC3, 0x1FAC5},
	{0x1FAF0, 0x1FAF8},
}

func isEmojiModifierBase(r rune) bool {
	lo, hi := 0, len(emojiModifierBaseRanges)-1
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		switch rng := emojiModifierBaseRanges[mid]; {
		case r < rng[0]:
			hi = mid - 1
		case r > rng[1]:
			lo = mid + 1
		default:
			return true
		}
	}
	return false
}

func isEmojiModifier(r rune) bool {
	return r >= emojiModifierFirst && r <= emojiModifierLast
}

// separateStandaloneEmojiModifiers inserts a zero-width space before every
// emoji modifier that does not follow an Emoji_Modifier_Base. A modifier that
// already starts a cluster (text start, after a control character or an
// inserted break) is left alone, so the transform is idempotent. A VS16
// between a base and its modifier is looked through: the pair still renders
// as one modified emoji.
func separateStandaloneEmojiModifiers(s string) string {
	if !strings.Contains(s, emojiModifierPrefix) {
		return s
	}
	var b strings.Builder
	prev := rune(-1)
	last := 0
	for i, r := range s {
		if isEmojiModifier(r) && prev >= 0x20 && prev != zeroWidthSpace && !isEmojiModifierBase(prev) {
			if b.Len() == 0 {
				b.Grow(len(s) + utf8.RuneLen(zeroWidthSpace))
			}
			b.WriteString(s[last:i])
			b.WriteRune(zeroWidthSpace)
			last = i
		}
		if r != variationSelector16 {
			prev = r
		}
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// joinStandaloneEmojiModifiers removes the cluster breaks
// separateStandaloneEmojiModifiers inserted, so text copied from the rendered
// screen matches the original.
func joinStandaloneEmojiModifiers(s string) string {
	marker := string(zeroWidthSpace) + emojiModifierPrefix
	if !strings.Contains(s, marker) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for i := strings.Index(s, marker); i >= 0; {
		next := i + len(string(zeroWidthSpace))
		if r, _ := utf8.DecodeRuneInString(s[next:]); isEmojiModifier(r) {
			b.WriteString(s[last:i])
			last = next
		}
		j := strings.Index(s[next:], marker)
		if j < 0 {
			break
		}
		i = next + j
	}
	b.WriteString(s[last:])
	return b.String()
}

// normalizeDisplayGlyphs rewrites the glyph sequences whose measured width
// disagrees with the terminal's advance: orphaned variation selectors are
// dropped and standalone emoji modifiers get their own cluster.
func normalizeDisplayGlyphs(s string) string {
	return separateStandaloneEmojiModifiers(tools.StripOrphanVariationSelectors(s))
}
