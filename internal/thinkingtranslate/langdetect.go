package thinkingtranslate

import (
	"unicode"
)

// scriptRatiosDetailed reports the ratio of semantic units in major scripts.
// It counts Latin words (consecutive Latin letters) vs Han/Kana/Hangul
// characters. This provides fairer weight: one English word = one CJK-script
// character.
func scriptRatiosDetailed(s string) (latinRatio, hanRatio, kanaRatio, hangulRatio float64) {
	var units, latinWords, hanChars, kanaChars, hangulChars int
	var inLatinWord bool

	for _, r := range s {
		if unicode.In(r, unicode.Han) {
			hanChars++
			inLatinWord = false
		} else if unicode.In(r, unicode.Hiragana, unicode.Katakana) {
			kanaChars++
			inLatinWord = false
		} else if unicode.In(r, unicode.Hangul) {
			hangulChars++
			inLatinWord = false
		} else if unicode.In(r, unicode.Latin) {
			if !inLatinWord {
				latinWords++
				inLatinWord = true
			}
		} else {
			inLatinWord = false
		}
	}

	units = latinWords + hanChars + kanaChars + hangulChars
	if units == 0 {
		return 0, 0, 0, 0
	}
	return float64(latinWords) / float64(units),
		float64(hanChars) / float64(units),
		float64(kanaChars) / float64(units),
		float64(hangulChars) / float64(units)
}

// scriptRatios preserves the legacy zh/en-oriented view used by existing
// heuristics: Latin words vs Han characters.
func scriptRatios(s string) (latinRatio float64, hanRatio float64) {
	latinRatio, hanRatio, _, _ = scriptRatiosDetailed(s)
	return latinRatio, hanRatio
}
