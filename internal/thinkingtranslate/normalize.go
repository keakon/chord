package thinkingtranslate

import (
	"strings"
	"unicode"
	"unicode/utf8"
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
	if originalRunes < 40 {
		return false
	}
	if translatedRunes <= 4 {
		return isMostlySymbols(translated)
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
