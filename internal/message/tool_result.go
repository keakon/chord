package message

import "strings"

// ToolResultClass is the tri-state classification of persisted tool output.
type ToolResultClass string

const (
	ToolResultClassSuccess   ToolResultClass = "success"
	ToolResultClassError     ToolResultClass = "error"
	ToolResultClassCancelled ToolResultClass = "cancelled"
)

// ClassifyToolResultContent classifies persisted tool output by the phrase
// conventions the runtime writes into results. It is the single source of the
// phrase list; boolean and tri-state consumers both delegate here.
func ClassifyToolResultContent(content string) ToolResultClass {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ToolResultClassSuccess
	}
	lower := strings.ToLower(trimmed)
	if lower == "cancelled" || strings.HasPrefix(lower, "cancelled\n") {
		return ToolResultClassCancelled
	}
	if strings.HasPrefix(trimmed, "Error: ") || strings.Contains(trimmed, "\n\nError: ") || strings.HasPrefix(trimmed, "Model stopped before completing this tool call") {
		return ToolResultClassError
	}
	return ToolResultClassSuccess
}

// ToolResultSucceeded reports whether persisted tool output represents a successful result.
func ToolResultSucceeded(content string) bool {
	return ClassifyToolResultContent(content) == ToolResultClassSuccess
}
