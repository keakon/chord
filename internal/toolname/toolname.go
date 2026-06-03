package toolname

import "strings"

const (
	Read         = "read"
	Write        = "write"
	Edit         = "edit"
	Delete       = "delete"
	Grep         = "grep"
	Glob         = "glob"
	WebFetch     = "web_fetch"
	Shell        = "shell"
	Spawn        = "spawn"
	SpawnStatus  = "spawn_status"
	SpawnStop    = "spawn_stop"
	Lsp          = "lsp"
	TodoWrite    = "todo_write"
	Question     = "question"
	Done         = "done"
	Delegate     = "delegate"
	Notify       = "notify"
	Skill        = "skill"
	Handoff      = "handoff"
	Escalate     = "escalate"
	Cancel       = "cancel"
	Complete     = "complete"
	SaveArtifact = "save_artifact"
	ReadArtifact = "read_artifact"
	ViewImage    = "view_image"
)

// Normalize trims user-provided tool names while preserving exact spelling.
func Normalize(name string) string {
	return strings.TrimSpace(name)
}
