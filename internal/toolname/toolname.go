package toolname

import "strings"

const (
	Read            = "read"
	Write           = "write"
	Edit            = "edit"
	ApplyPatch      = "apply_patch"
	Delete          = "delete"
	Grep            = "grep"
	Glob            = "glob"
	WebFetch        = "web_fetch"
	Shell           = "shell"
	Spawn           = "spawn"
	SpawnStatus     = "spawn_status"
	SpawnStop       = "spawn_stop"
	Lsp             = "lsp"
	TodoWrite       = "todo_write"
	Question        = "question"
	Done            = "done"
	Delegate        = "delegate"
	Notify          = "notify"
	Skill           = "skill"
	Handoff         = "handoff"
	Escalate        = "escalate"
	Cancel          = "cancel"
	Complete        = "complete"
	TaskCollect     = "task_collect"
	TaskGroupCreate = "task_group_create"
	SaveArtifact    = "save_artifact"
	ReadArtifact    = "read_artifact"
	SaveResult      = "save_result"
	ViewImage       = "view_image"
)

// Normalize trims user-provided tool names and maps legacy aliases.
func Normalize(name string) string {
	name = strings.TrimSpace(name)
	if name == "patch" {
		return ApplyPatch
	}
	return name
}
