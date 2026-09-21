package tools

import (
	"github.com/keakon/chord/internal/toolname"
)

const (
	NameRead           = toolname.Read
	NameWrite          = toolname.Write
	NameEdit           = toolname.Edit
	NameApplyPatch     = toolname.ApplyPatch
	NameDelete         = toolname.Delete
	NameGrep           = toolname.Grep
	NameGlob           = toolname.Glob
	NameWebFetch       = toolname.WebFetch
	NameShell          = toolname.Shell
	NameJobOutput      = toolname.JobOutput
	NameJobList        = toolname.JobList
	NameJobKill        = toolname.JobKill
	NameLsp            = toolname.Lsp
	NameTodoWrite      = toolname.TodoWrite
	NameQuestion       = toolname.Question
	NameDone           = toolname.Done
	NameDelegate       = toolname.Delegate
	NameNotify         = toolname.Notify
	NameSkill          = toolname.Skill
	NameHandoff        = toolname.Handoff
	NameEscalate       = toolname.Escalate
	NameCancel         = toolname.Cancel
	NameComplete       = toolname.Complete
	NameSaveArtifact   = toolname.SaveArtifact
	NameReadArtifact   = toolname.ReadArtifact
	NameViewImage      = toolname.ViewImage
	NameCompactContext = toolname.CompactContext
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

// IsConsumingRead reports whether a read-only tool consumes state as it runs, so
// a crash between its execution and its persisted result cannot be treated as
// "never started, safe to retry". job_output is the only such read today: it
// advances the calling agent's output cursor and can claim the job's completion
// notification, so a replay returns an empty window instead of the output the
// interrupted call had already taken.
func IsConsumingRead(name string) bool {
	return NormalizeName(name) == NameJobOutput
}

// IsFileMutation reports whether the tool mutates files in the workspace.
func IsFileMutation(name string) bool {
	switch NormalizeName(name) {
	case NameWrite, NameEdit, NameApplyPatch, NameDelete:
		return true
	default:
		return false
	}
}

// IsFileStateTool reports whether the tool can emit tracked file state metadata.
func IsFileStateTool(name string) bool {
	switch NormalizeName(name) {
	case NameRead, NameWrite, NameEdit, NameApplyPatch, NameDelete:
		return true
	default:
		return false
	}
}
