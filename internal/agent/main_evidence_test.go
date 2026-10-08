package agent

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// A diff that only covers agent-owned note/plan documents is task state the
// checkpoint re-loads as a state file, not a code change. Every uncertain or
// mixed case must keep its diff: dropping a real code change saves a few
// tokens and leaves the continuation reasoning from a missing premise.
func TestNotesStateOnlyToolDiffClassifiesNoteWrites(t *testing.T) {
	tests := []struct {
		name  string
		state *message.ToolFileState
		want  bool
	}{
		{name: "no recorded state", state: nil, want: false},
		{name: "reads only", state: &message.ToolFileState{Reads: []message.TrackedFileState{{Path: ".chord/notes/task.md"}}}, want: false},
		{name: "deletes only", state: &message.ToolFileState{Deletes: []message.TrackedFileState{{Path: ".chord/notes/task.md"}}}, want: false},
		{name: "code write", state: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "internal/agent/main_evidence.go"}}}, want: false},
		{name: "notes write", state: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: ".chord/notes/task.md"}}}, want: true},
		{name: "plans write with dot prefix", state: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: "./.chord/plans/plan.md"}}}, want: true},
		{name: "absolute notes change", state: &message.ToolFileState{Changes: []message.ToolFileChange{{Path: "/repo/.chord/notes/task.md"}}}, want: true},
		{name: "move into plans", state: &message.ToolFileState{Changes: []message.ToolFileChange{{Path: "draft.md", TargetPath: ".chord/plans/final.md"}}}, want: false},
		{name: "move out of notes", state: &message.ToolFileState{Changes: []message.ToolFileChange{{Path: ".chord/notes/draft.md", TargetPath: "docs/final.md"}}}, want: false},
		{name: "notes plus code", state: &message.ToolFileState{Writes: []message.TrackedFileState{{Path: ".chord/notes/task.md"}, {Path: "internal/agent/main_evidence.go"}}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := message.Message{
				Role:       message.RoleTool,
				Content:    "applied",
				ToolStatus: message.ToolStatusSuccess,
				ToolDiff:   "--- a.md\n+++ a.md\n@@ -1,0 +1,1 @@\n+line",
				FileState:  tt.state,
			}
			if got := notesStateOnlyToolDiff(msg); got != tt.want {
				t.Fatalf("notesStateOnlyToolDiff = %v, want %v (state %#v)", got, tt.want, tt.state)
			}
		})
	}
}

// Both evidence build paths — the reconstruction scan and the live tracker —
// must agree on a notes-only diff, or one of them keeps feeding the pack an
// excerpt the other drops.
func TestEvidenceBuildPathsSkipNotesOnlyToolDiff(t *testing.T) {
	notesWrite := message.Message{
		Role:       message.RoleTool,
		ToolCallID: "notes-write",
		Content:    "applied",
		ToolStatus: message.ToolStatusSuccess,
		ToolDiff:   "--- .chord/notes/task.md\n+++ .chord/notes/task.md\n@@ -4,0 +5,1 @@\n+next step: rerun the focused tests",
		FileState: &message.ToolFileState{
			Writes: []message.TrackedFileState{{Path: ".chord/notes/task.md", SHA256: "notes-rev", Exists: true}},
		},
	}
	codeWrite := message.Message{
		Role:       message.RoleTool,
		ToolCallID: "code-write",
		Content:    "applied",
		ToolStatus: message.ToolStatusSuccess,
		ToolDiff:   "--- internal/agent/main_evidence.go\n+++ internal/agent/main_evidence.go\n@@ -1,0 +1,1 @@\n+func notesStateOnlyToolDiff()",
		FileState: &message.ToolFileState{
			Writes: []message.TrackedFileState{{Path: "internal/agent/main_evidence.go", SHA256: "code-rev", Exists: true}},
		},
	}

	scanned := collectEvidenceItems([]message.Message{notesWrite, codeWrite})
	scannedDiffs := 0
	for _, item := range scanned {
		if item.Kind != evidenceToolDiff {
			continue
		}
		scannedDiffs++
		if strings.Contains(item.Excerpt, "next step: rerun the focused tests") {
			t.Fatalf("reconstruction path kept a notes-only diff:\n%s", item.Excerpt)
		}
	}
	if scannedDiffs != 1 {
		t.Fatalf("reconstruction path kept %d diffs, want only the code diff: %#v", scannedDiffs, scanned)
	}

	a := &MainAgent{}
	a.recordEvidenceFromMessage(notesWrite)
	if tracked := a.evidence.snapshot(); len(tracked) != 0 {
		t.Fatalf("tracker kept %d items for a notes-only diff, want none: %#v", len(tracked), tracked)
	}
	a.recordEvidenceFromMessage(codeWrite)
	tracked := a.evidence.snapshot()
	if len(tracked) != 1 || tracked[0].Kind != evidenceToolDiff {
		t.Fatalf("tracker items = %#v, want the code diff only", tracked)
	}
}
