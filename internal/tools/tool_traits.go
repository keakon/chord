package tools

import "strings"

const (
	NameRead     = "Read"
	NameWrite    = "Write"
	NameEdit     = "Edit"
	NameDelete   = "Delete"
	NameGrep     = "Grep"
	NameGlob     = "Glob"
	NameWebFetch = "WebFetch"
)

// IsReadLike reports whether tool output should be treated as read-only context
// that can be compacted into a re-runnable summary.
func IsReadLike(name string) bool {
	switch strings.TrimSpace(name) {
	case NameRead, NameGrep, NameGlob, NameWebFetch:
		return true
	default:
		return false
	}
}

// IsFileMutation reports whether the tool mutates files in the workspace.
func IsFileMutation(name string) bool {
	switch strings.TrimSpace(name) {
	case NameWrite, NameEdit, NameDelete:
		return true
	default:
		return false
	}
}

// ShouldExpandResult reports whether TUI should expand the tool result by default.
func ShouldExpandResult(name string) bool {
	name = strings.TrimSpace(name)
	return name == NameRead || IsFileMutation(name)
}
