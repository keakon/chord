package message

import "strings"

// ToolResultSucceeded reports whether persisted tool output represents a successful result.
func ToolResultSucceeded(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	if lower == "cancelled" || strings.HasPrefix(lower, "cancelled\n") {
		return false
	}
	if strings.HasPrefix(trimmed, "Error: ") || strings.Contains(trimmed, "\n\nError: ") || strings.HasPrefix(trimmed, "Model stopped before completing this tool call") {
		return false
	}
	return true
}
