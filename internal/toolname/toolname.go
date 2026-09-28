package toolname

import (
	"strings"
	"unicode"
)

const maxToolNameLength = 128

const (
	Read           = "read"
	Write          = "write"
	Edit           = "edit"
	ApplyPatch     = "apply_patch"
	Delete         = "delete"
	Grep           = "grep"
	Glob           = "glob"
	WebFetch       = "web_fetch"
	Shell          = "shell"
	JobOutput      = "job_output"
	JobList        = "job_list"
	JobKill        = "job_kill"
	TodoWrite      = "todo_write"
	Question       = "question"
	Done           = "done"
	Delegate       = "delegate"
	Notify         = "notify"
	Skill          = "skill"
	Handoff        = "handoff"
	Escalate       = "escalate"
	Cancel         = "cancel"
	Complete       = "complete"
	SaveArtifact   = "save_artifact"
	ReadArtifact   = "read_artifact"
	ViewImage      = "view_image"
	CompactContext = "compact_context"
	WorktreeEnter  = "worktree_enter"
	WorktreeExit   = "worktree_exit"
	WorktreeList   = "worktree_list"
)

// MCPToolPrefix marks the ids of dynamically registered MCP tools, whose shape
// is mcp_<server>_<tool>. Producer and matchers share it so a naming change
// cannot leave one side recognizing names the other no longer produces.
const MCPToolPrefix = "mcp_"

// ApplyPatchHunkGuidance is the apply_patch hunk-sizing rule. It is part of
// both the JSON function description (tools) and the freeform custom-tool wire
// description (llm), which otherwise replaces the whole description with the
// Codex text; the two packages share it here so the rule has one source.
const ApplyPatchHunkGuidance = "Prefer the smallest hunk with distinctive context — in repeated blocks such as tests or fixtures, put the enclosing function, test, or case name on the `@@` line; after a mismatch, re-read the current target range and rebuild the hunk instead of retrying it unchanged."

// Normalize trims user-provided tool names and maps legacy aliases.
func Normalize(name string) string {
	name = strings.TrimSpace(name)
	if name == "patch" {
		return ApplyPatch
	}
	return name
}

// IsValid reports whether name can be a wire-level tool identifier. Tool
// names are provider-controlled input, so keep them bounded and deliberately
// narrower than free text before they reach history, routing, or permissions.
// Registered tools are validated by identity at dispatch. This bounded syntax
// check rejects malformed unknown names without treating ordinary unknown tools
// as a wire parsing failure. Unicode letters support MCP naming.
func IsValid(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxToolNameLength {
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) ||
			r == '_' || r == '.' || r == ':' || r == '-' {
			continue
		}
		return false
	}
	return true
}
