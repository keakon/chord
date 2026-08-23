package llm

import "strings"

// joinAdjacentPartText inserts a newline between separately-authored text
// parts only when neither side already contributes one.
func joinAdjacentPartText(prev, text string) string {
	if prev == "" {
		return text
	}
	if text == "" {
		return prev
	}
	if !strings.HasSuffix(prev, "\n") && !strings.HasPrefix(text, "\n") {
		prev += "\n"
	}
	return prev + text
}
