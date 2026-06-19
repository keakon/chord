package tools

import (
	"github.com/keakon/chord/internal/toolname"
)

const (
	NameRead         = toolname.Read
	NameWrite        = toolname.Write
	NameEdit         = toolname.Edit
	NamePatch        = toolname.Patch
	NameDelete       = toolname.Delete
	NameGrep         = toolname.Grep
	NameGlob         = toolname.Glob
	NameWebFetch     = toolname.WebFetch
	NameShell        = toolname.Shell
	NameSpawn        = toolname.Spawn
	NameSpawnStatus  = toolname.SpawnStatus
	NameSpawnStop    = toolname.SpawnStop
	NameLsp          = toolname.Lsp
	NameTodoWrite    = toolname.TodoWrite
	NameQuestion     = toolname.Question
	NameDone         = toolname.Done
	NameDelegate     = toolname.Delegate
	NameNotify       = toolname.Notify
	NameSkill        = toolname.Skill
	NameHandoff      = toolname.Handoff
	NameEscalate     = toolname.Escalate
	NameCancel       = toolname.Cancel
	NameComplete     = toolname.Complete
	NameSaveArtifact = toolname.SaveArtifact
	NameReadArtifact = toolname.ReadArtifact
	NameViewImage    = toolname.ViewImage
)

var NormalizeName = toolname.Normalize

// IsReadLike reports whether tool output should be treated as read-only context
// that can be compacted into a re-runnable summary.
func IsReadLike(name string) bool {
	switch NormalizeName(name) {
	case NameRead, NameGrep, NameGlob, NameWebFetch:
		return true
	default:
		return false
	}
}

// IsFileMutation reports whether the tool mutates files in the workspace.
func IsFileMutation(name string) bool {
	switch NormalizeName(name) {
	case NameWrite, NameEdit, NamePatch, NameDelete:
		return true
	default:
		return false
	}
}

// ShouldExpandResult reports whether TUI should expand the tool result by default.
func ShouldExpandResult(name string) bool {
	name = NormalizeName(name)
	return name == NameRead || IsFileMutation(name)
}
