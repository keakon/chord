package message

import (
	"strings"
)

// Tool results close on exactly one terminal status: success, error, or
// cancelled. The predicates below are the single vocabulary for asking "how did
// this tool result end", so callers pick a semantic instead of re-deriving one
// from the raw string. The distinction between them is deliberate:
//
//   - IsToolResultErrorStatus: only an explicit failure. Use it where a
//     cancelled result should keep behaving like a completed one, for example
//     when deciding whether a mutating command may have already touched the
//     workspace.
//   - IsToolResultUnsuccessfulStatus: failure or cancellation. Use it wherever
//     treating an unfinished result as trustworthy would be unsafe, such as
//     retaining a read as the current view of a file.
//   - IsToolResultSuccessStatus: an explicit success only. Use it when an
//     unknown or missing status must not be assumed good.
//
// Empty status means the transcript predates terminal-status persistence;
// callers that must handle those decide their own fallback.

// IsSuccess reports an explicit success only; empty or unknown statuses are
// not assumed good. Exported so out-of-package callers (e.g. the TUI) reuse
// this vocabulary instead of re-deriving it from the raw string.
func (s ToolResultStatus) IsSuccess() bool {
	return IsToolResultSuccessStatus(string(s))
}

// IsUnsuccessful reports an explicit failure or cancellation. Exported for the
// same reason as IsSuccess.
func (s ToolResultStatus) IsUnsuccessful() bool {
	return IsToolResultUnsuccessfulStatus(string(s))
}

func IsToolResultErrorStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), ToolStatusError)
}

func IsToolResultUnsuccessfulStatus(status string) bool {
	status = strings.TrimSpace(status)
	return strings.EqualFold(status, ToolStatusError) ||
		strings.EqualFold(status, ToolStatusCancelled)
}

func IsToolResultSuccessStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), ToolStatusSuccess)
}

// IsToolResultErrorMessage also sniffs the rendered content because imported
// and legacy transcripts can carry a failure without a terminal status. An
// explicit terminal status takes precedence: successful output may legitimately
// contain the text "Error:" in source code, test data, or command output.
func IsToolResultErrorMessage(msg Message) bool {
	switch strings.ToLower(strings.TrimSpace(msg.ToolStatus)) {
	case ToolStatusError:
		return true
	case ToolStatusSuccess, ToolStatusCancelled:
		return false
	default:
		return IsToolErrorContent(msg.Content)
	}
}

// IsToolErrorContent reports a failure recorded in the rendered content itself.
// It delegates to the central classifier so the phrase list and the appended
// separator have exactly one definition.
func IsToolErrorContent(content string) bool {
	return ClassifyToolResultContent(content) == ToolResultClassError
}

// ToolResultStatus is the terminal state of a tool call for UI/protocol purposes.
type ToolResultStatus string
