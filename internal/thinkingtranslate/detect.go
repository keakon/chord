package thinkingtranslate

import (
	"strings"
)

type DetectFunc func(text string) (lang string, confidence float64)

// GuessUserLang is a best-effort heuristic to infer the user's language from a
// user-authored message. It returns fallback when uncertain.
func GuessUserLang(userText string, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		fallback = "zh"
	}
	latin, han := scriptRatios(userText)
	// rough heuristic: if there's meaningful Han usage, assume Chinese.
	if han >= 0.30 {
		return "zh"
	}
	if latin >= 0.70 {
		return "en"
	}
	return fallback
}
