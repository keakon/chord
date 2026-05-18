package thinkingtranslate

import "strings"

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
	return s
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
