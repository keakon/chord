package thinkingtranslate

import (
	"unicode"
)

// scriptRatios reports the ratio of letters in major scripts.
// Currently only Han and Latin are used for triggering translation.
func scriptRatios(s string) (latinRatio float64, hanRatio float64) {
	var letters, latin, han int
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		switch {
		case unicode.In(r, unicode.Han):
			han++
		case unicode.In(r, unicode.Latin):
			latin++
		}
	}
	if letters == 0 {
		return 0, 0
	}
	return float64(latin) / float64(letters), float64(han) / float64(letters)
}
