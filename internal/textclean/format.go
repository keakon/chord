// Package textclean removes content-free Unicode formatting while preserving
// meaningful emoji sequences and leading byte-order marks.
package textclean

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CombiningEnclosingKeycap completes an emoji keycap sequence.
const CombiningEnclosingKeycap = '\u20e3'

// StripOrphanVariationSelectors drops variation selectors (U+FE0E/U+FE0F) that
// are not preceded by a base character able to carry an emoji or text
// presentation. Models occasionally emit these orphans (e.g. "️0" instead of
// "0"): inside edit/apply_patch arguments they make matching fail because the
// file content does not contain the selector. Rendered text also drops these
// marks while keeping valid presentation sequences intact.
//
// Stripping a selector that was *not* orphaned costs a column in the other
// direction — "1️⃣" measures 2 and paints 2, while a stripped "1⃣" measures 1
// and still paints 2 — so canTakeVariationSelector follows Unicode's own list
// of bases rather than a looser guess.
func StripOrphanVariationSelectors(s string) string {
	if !strings.ContainsRune(s, '\ufe0f') && !strings.ContainsRune(s, '\ufe0e') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prev := rune(-1)
	for i, r := range s {
		if r == '\ufe0f' || r == '\ufe0e' {
			// Keycap bases carry a selector only as part of the full sequence,
			// so they need the one rune of lookahead the category test cannot
			// do: "1\ufe0f\u20e3" is an emoji, a bare "1\ufe0f" is a defective
			// keycap and in practice an orphan.
			keycap := r == '\ufe0f' && isKeycapBase(prev) &&
				strings.HasPrefix(s[i+utf8.RuneLen(r):], string(CombiningEnclosingKeycap))
			if !keycap && !canTakeVariationSelector(prev) {
				continue
			}
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// StripZeroWidthFormat drops the zero-width formatting characters that models
// leak into tool arguments as output corruption — a ZWSP inside an operator
// like "a - \u200bb" silently breaks string matching and planting it in a
// written file leaves invisible bytes behind. These runes carry no content:
// removing them cannot change what the text means, only which invisible
// formatting hints survive. Inside code or markup they
// are never intended, and writing them would plant invisible bytes in the
// file; strip them the same way orphaned variation selectors are stripped,
// so matching and writing both succeed on the visible text.
//
// The stripped set is only the truly content-free format runes — ZWSP, ZWNJ,
// ZWJ, word joiner, BOM (non-leading), soft hyphen, and the variation
// selectors U+FE00–U+FE0D — and deliberately excludes the space characters
// (NBSP, figure/ideographic spaces, narrow NBSP) that carry visible width and
// can be intended. U+FE0E/U+FE0F are handled by StripOrphanVariationSelectors,
// which keeps them when a base character legitimately carries them; U+FE00–
// U+FE0D select only CJK-compatible ideographs and have no meaning in the
// plain text and code these tools write, so they are stripped unconditionally.
func StripZeroWidthFormat(s string) string {
	if !strings.ContainsAny(s, "\u200b\u200c\u200d\u2060\ufeff\u00ad\ufe00\ufe01\ufe02\ufe03\ufe04\ufe05\ufe06\ufe07\ufe08\ufe09\ufe0a\ufe0b\ufe0c\ufe0d") {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		switch {
		case r == '\u200d':
			// A ZWJ joins two emoji into a single glyph (family, flag,
			// profession sequences); anywhere else it is corruption, same as
			// a bare ZWSP.
			if isEmojiBaseRune(emojiNeighbor(runes, i, -1)) && isEmojiBaseRune(emojiNeighbor(runes, i, +1)) {
				b.WriteRune(r)
			}
		case r == '\ufeff' && i == 0:
			b.WriteRune(r) // leading BOM is byte-order encoding, not corruption
		case IsZeroWidthFormatRune(r):
			// strip
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isEmojiBaseRune reports whether r can carry a ZWJ sequence: the emoji
// blocks (U+1F000+) plus Other/Modifier symbols that host emoji bases.
func isEmojiBaseRune(r rune) bool {
	return r >= 0x1F000 || unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r)
}

// emojiNeighbor walks from index i in the given direction (±1) to the nearest
// rune that can anchor an emoji sequence, skipping runes that attach to an
// emoji base without being one — the variation selectors U+FE0E/U+FE0F and
// the skin-tone modifiers U+1F3FB–U+1F3FF — and over the content-free runes
// this strip removes, so sequences like 🏳️‍🌈 (base + VS16 + ZWJ + base)
// and 👨ZWSPZWJ👩 keep their ZWJ. ZWJ itself is not skipped, so a doubled
// ZWJ still fails the joiner check on both sides. Returns 0 at the string
// edge; 0 is never an emoji base.
func emojiNeighbor(runes []rune, i, step int) rune {
	for j := i + step; j >= 0 && j < len(runes); j += step {
		r := runes[j]
		if (IsZeroWidthFormatRune(r) && r != '\u200d') || r == '\ufe0e' || r == '\ufe0f' || (r >= 0x1f3fb && r <= 0x1f3ff) {
			continue
		}
		return r
	}
	return 0
}

// IsZeroWidthFormatRune reports whether r is a zero-width formatting rune
// stripped by StripZeroWidthFormat: ZWSP, ZWNJ, ZWJ, word joiner, BOM, soft
// hyphen, and the variation selectors U+FE00–U+FE0D (CJK-compatible-only
// selectors with no meaning in plain text and code). ZWJ is preserved only
// when it joins two emoji (👩‍👩‍👧 family sequences are real content); a
// standalone ZWJ, or one joining non-emoji, is model corruption like a bare
// ZWSP. BOM is stripped only in mid-stream positions — a leading BOM is
// byte-order encoding, not model corruption, and is preserved by callers
// (readFileForEdit detects it). U+FE0E/U+FE0F are excluded here: they can be
// carried by a base character and are handled by StripOrphanVariationSelectors.
func IsZeroWidthFormatRune(r rune) bool {
	switch {
	case r >= '\ufe00' && r <= '\ufe0d':
		return true
	case r == '\u200b' || r == '\u200c' || r == '\u200d' || r == '\u2060' || r == '\ufeff' || r == '\u00ad':
		return true
	}
	return false
}

// isKeycapBase reports whether r is one of the twelve bases of a keycap emoji
// (# * 0-9 followed by U+FE0F U+20E3). They are the only ASCII characters
// Unicode gives an emoji variation sequence, and canTakeVariationSelector
// deliberately excludes them because they are valid only with the U+20E3.
func isKeycapBase(r rune) bool {
	return r == '#' || r == '*' || (r >= '0' && r <= '9')
}

// canTakeVariationSelector reports whether r may legitimately be followed by an
// emoji/text presentation selector. Unicode standardizes 371 such bases in
// emoji-variation-sequences.txt; 354 of them are symbols, so the symbol
// categories carry the rule — So and Sk cover U+26A0 WARNING SIGN in "⚠️" and
// the rest of the misc-symbols bases, and Sm is required too because the arrow
// emoji are split across categories (U+2195 "↕️" is So while U+2194 "↔️" is Sm).
// Of the seventeen non-symbol bases, twelve are the keycap ones the caller
// handles and the five listed here are classed as punctuation, a dash, or even
// a letter (U+2139 "ℹ️").
//
// ASCII is rejected outright: its only bases are the keycap ones, so a selector
// after Sm members such as "+" or "=" is an orphan — "+ ️1", a dropped emoji
// next to a plus, is one of the shapes actually observed.
//
// The rule still over-allows, since So/Sk/Sm hold far more members than the 354
// symbol bases: a selector orphaned after "°" or "±" survives. That is the
// deliberate direction. Both mistakes cost a column — a kept orphan is measured
// one wider than it paints, a wrongly stripped selector one narrower — but the
// kept orphan leaves the text intact, while an over-eager strip rewrites what
// the model wrote.
func canTakeVariationSelector(r rune) bool {
	if r < 0x80 {
		return false
	}
	switch r {
	case '‼', '⁉', 'ℹ', '〰', '〽':
		return true
	}
	return unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) || unicode.Is(unicode.Sm, r)
}
