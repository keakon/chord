package requesttrim

import (
	"strings"

	"github.com/keakon/chord/internal/texttruncate"
)

// ToolCall owns normalized call metadata and a lazily parsed read request. Copies may
// populate their own parse cache without changing the persisted message.
type ToolCall struct {
	Name              string
	Args              string
	readRequest       ReadRequest
	readRequestParsed bool
}

// ParsedReadRequest returns read arguments and caches their parse on this metadata value.
func (m *ToolCall) ParsedReadRequest() ReadRequest {
	if !m.readRequestParsed {
		m.readRequest = parseReadRequestSummary(m.Args)
		m.readRequestParsed = true
	}
	return m.readRequest
}

// TextSnippet bounds a trimmed excerpt by retaining its head and tail.
func TextSnippet(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	if s == "" || maxChars <= 0 {
		return ""
	}
	keepHead := maxChars * 2 / 3
	if keepHead < 1 {
		keepHead = maxChars
	}
	sep := "\n...\n"
	keepTail := max(maxChars-keepHead-len([]rune(sep)), 0)
	if keepTail == 0 {
		return strings.TrimSpace(texttruncate.Runes(s, keepHead, ""))
	}
	return strings.TrimSpace(texttruncate.HeadTail(s, keepHead, keepTail, sep))
}

func isConfirmationOutput(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || len(trimmed) > 180 {
		return false
	}
	if strings.Count(trimmed, "\n") > 2 {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, phrase := range []string{
		"applied successfully",
		"completed successfully",
		"written successfully",
		"updated successfully",
		"created successfully",
		"removed successfully",
		"saved successfully",
		"background task started",
		"background task completed",
		"done",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// ToolNameOrUnknown returns a normalized tool name for evidence labels.
func ToolNameOrUnknown(name string) string {
	if name == "" {
		return "tool"
	}
	return name
}
