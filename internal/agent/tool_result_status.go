package agent

import (
	"strings"

	"github.com/keakon/chord/internal/message"
)

// Tool results close on exactly one terminal status: success, error, or
// cancelled. The predicates below are the single vocabulary for asking "how did
// this tool result end", so callers pick a semantic instead of re-deriving one
// from the raw string. The distinction between them is deliberate:
//
//   - isToolResultErrorStatus: only an explicit failure. Use it where a
//     cancelled result should keep behaving like a completed one, for example
//     when deciding whether a mutating command may have already touched the
//     workspace.
//   - isToolResultUnsuccessfulStatus: failure or cancellation. Use it wherever
//     treating an unfinished result as trustworthy would be unsafe, such as
//     retaining a read as the current view of a file.
//   - isToolResultSuccessStatus: an explicit success only. Use it when an
//     unknown or missing status must not be assumed good.
//
// Empty status means the transcript predates terminal-status persistence;
// callers that must handle those decide their own fallback.

func isToolResultErrorStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), string(ToolResultStatusError))
}

func isToolResultUnsuccessfulStatus(status string) bool {
	status = strings.TrimSpace(status)
	return strings.EqualFold(status, string(ToolResultStatusError)) ||
		strings.EqualFold(status, string(ToolResultStatusCancelled))
}

func isToolResultSuccessStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), string(ToolResultStatusSuccess))
}

// isToolResultErrorMessage also sniffs the rendered content because imported
// and legacy transcripts can carry a failure without a terminal status.
func isToolResultErrorMessage(msg message.Message) bool {
	return isToolResultErrorStatus(msg.ToolStatus) || strings.Contains(msg.Content, "Error:")
}

func isToolErrorContent(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), "Error:")
}
