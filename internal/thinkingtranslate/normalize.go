package thinkingtranslate

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxTranslationWordCountRatio = 4
	minTranslationWordsForRatio  = 8
)

// NormalizeForCompare is used to decide whether a translation is meaningfully
// different from the original.
func NormalizeForCompare(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

// ExtractTranslationEnvelope returns the translated payload from the structured
// response format requested by translationPrompt. If the model does not follow
// the format, it returns the trimmed response unchanged so existing bare-text
// responses remain accepted.
func ExtractTranslationEnvelope(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if inner, ok := extractEnvelope(s, "TRANSLATION"); ok {
		return inner
	}
	if inner, ok := extractOpenEnvelope(s, "TRANSLATION"); ok {
		return inner
	}
	return s
}

func IsClearlyInvalidTranslation(original, targetLang, translated string) bool {
	original = NormalizeForCompare(original)
	translated = NormalizeForCompare(translated)
	if translated == "" {
		return true
	}
	originalRunes := utf8.RuneCountInString(original)
	translatedRunes := utf8.RuneCountInString(translated)
	if translatedRunes <= 4 && isMostlySymbols(translated) {
		return true
	}
	if hasExcessiveWordCountRatio(original, translated, maxTranslationWordCountRatio) {
		return true
	}
	if originalRunes < 40 {
		return false
	}
	if translatedRunes >= 100 || translatedRunes*5 >= originalRunes {
		return isWrongTargetScript(targetLang, translated)
	}
	if isWrongTargetScript(targetLang, translated) {
		return true
	}
	if originalRunes >= 100 && translatedRunes <= 20 && translatedRunes*8 < originalRunes {
		return true
	}
	return false
}

func hasExcessiveWordCountRatio(original, translated string, maxRatio int) bool {
	if maxRatio <= 1 {
		return false
	}
	originalWords := approximateWordCount(original)
	translatedWords := approximateWordCount(translated)
	if originalWords == 0 || translatedWords == 0 {
		return false
	}
	if originalWords < minTranslationWordsForRatio && translatedWords < minTranslationWordsForRatio {
		return false
	}
	return originalWords > translatedWords*maxRatio || translatedWords > originalWords*maxRatio
}

func approximateWordCount(s string) int {
	words := 0
	cjkRunes := 0
	inWord := false
	flushCJK := func() {
		if cjkRunes > 0 {
			words += (cjkRunes + 1) / 2
			cjkRunes = 0
		}
	}
	flushWord := func() {
		if inWord {
			words++
			inWord = false
		}
	}
	for _, r := range s {
		switch {
		case isCJKRune(r):
			flushWord()
			cjkRunes++
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			inWord = true
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return words
}

func isCJKRune(r rune) bool {
	return (r >= 0x3400 && r <= 0x9FFF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x3040 && r <= 0x30FF) ||
		(r >= 0xAC00 && r <= 0xD7AF)
}

func isWrongTargetScript(targetLang, translated string) bool {
	targetLang = strings.ToLower(strings.TrimSpace(targetLang))
	if !strings.HasPrefix(targetLang, "zh") {
		return false
	}
	latin, han := scriptRatios(translated)
	return han < 0.20 && latin >= 0.70
}

func isMostlySymbols(s string) bool {
	nonSpace := 0
	symbols := 0
	lettersOrDigits := 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			continue
		}
		nonSpace++
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			lettersOrDigits++
			continue
		}
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			symbols++
		}
	}
	if nonSpace == 0 {
		return true
	}
	return lettersOrDigits == 0 && symbols*2 >= nonSpace
}

func extractEnvelope(s, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	upper := strings.ToUpper(s)
	start := strings.Index(upper, open)
	if start < 0 {
		return "", false
	}
	contentStart := start + len(open)
	endRel := strings.Index(upper[contentStart:], close)
	if endRel < 0 {
		return "", false
	}
	end := contentStart + endRel
	return strings.TrimSpace(s[contentStart:end]), true
}

func extractOpenEnvelope(s, tag string) (string, bool) {
	open := "<" + tag + ">"
	upper := strings.ToUpper(s)
	if !strings.HasPrefix(upper, open) {
		return "", false
	}
	return strings.TrimSpace(s[len(open):]), true
}
