package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

func TestLegacyPatchEventsUseStableApplyPatchDisplay(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`

	m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "call-1", Name: "patch", ArgsJSON: args})
	m.handleToolAgentEvent(agent.ToolCallExecutionEvent{ID: "call-1", Name: "patch", ArgsJSON: args, State: agent.ToolCallExecutionStateRunning})
	m.handleToolAgentEvent(agent.ToolResultEvent{
		CallID:   "call-1",
		Name:     "patch",
		ArgsJSON: args,
		Result:   "Applied patch",
		Status:   agent.ToolResultStatusSuccess,
		Diff:     "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
	})

	block, ok := m.findToolBlockByToolID("call-1")
	if !ok {
		t.Fatal("missing tool block")
	}
	if block.ToolName != tools.NameApplyPatch {
		t.Fatalf("ToolName = %q, want %q", block.ToolName, tools.NameApplyPatch)
	}
	if block.Content != `{"paths":["src/demo.go"]}` {
		t.Fatalf("Content = %q, want stable path-only display args", block.Content)
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected normalized name, path, and diff, got:\n%s", plain)
	}
}

func TestApplyPatchDeleteUsesDDisplayAndHidesDiff(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	args := `{"patch":"*** Begin Patch\n*** Delete File: tmp/old.txt\n*** End Patch"}`

	m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "call-delete", Name: tools.NameApplyPatch, ArgsJSON: args})
	m.handleToolAgentEvent(agent.ToolResultEvent{
		CallID: "call-delete", Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "Applied patch:\nD tmp/old.txt", Status: agent.ToolResultStatusSuccess,
		Diff: "--- tmp/old.txt\n+++ /dev/null\n@@ -1,2 +0,0 @@\n-old\n-content\n",
	})

	block, ok := m.findToolBlockByToolID("call-delete")
	if !ok {
		t.Fatal("missing tool block")
	}
	if block.Content != `{"paths":["D tmp/old.txt"]}` {
		t.Fatalf("Content = %q, want deletion marker", block.Content)
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "apply_patch D tmp/old.txt") {
		t.Fatalf("expected deletion marker in header, got:\n%s", plain)
	}
	if strings.Contains(plain, "-old") || strings.Contains(plain, "-content") {
		t.Fatalf("expected deleted file diff to be hidden, got:\n%s", plain)
	}
}

func TestApplyPatchResultTracksChangedFiles(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** Add File: docs/new.md\n+# New\n*** Delete File: tmp/old.txt\n*** Update File: src/old.go\n*** Move to: src/new.go\n@@\n-before\n+after\n*** End Patch"}`

	m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args})
	m.handleToolAgentEvent(agent.ToolResultEvent{
		CallID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "Applied patch", Status: agent.ToolResultStatusSuccess,
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 5 {
		t.Fatalf("changed files = %d, want 5: %+v", len(edits), edits)
	}
	assertFileEdit(t, edits[0], "src/demo.go", 1, 1, false)
	assertFileEdit(t, edits[1], "docs/new.md", 1, 0, false)
	assertFileEdit(t, edits[2], "tmp/old.txt", 0, 0, true)
	assertFileEdit(t, edits[3], "src/old.go", 0, 0, true)
	assertFileEdit(t, edits[4], "src/new.go", 1, 1, false)
}

func TestFailedApplyPatchResultDoesNotTrackChangedFiles(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`

	m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args})
	m.handleToolAgentEvent(agent.ToolResultEvent{
		CallID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "No files were modified", Status: agent.ToolResultStatusError,
	})

	if edits := m.sidebar.CurrentAgentFiles(); len(edits) != 0 {
		t.Fatalf("failed apply_patch changed files = %+v, want none", edits)
	}
}

func TestCancelledApplyPatchResultDoesNotTrackChangedFiles(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`

	m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args})
	m.handleToolAgentEvent(agent.ToolResultEvent{
		CallID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "Cancelled", Status: agent.ToolResultStatusCancelled,
	})

	if edits := m.sidebar.CurrentAgentFiles(); len(edits) != 0 {
		t.Fatalf("cancelled apply_patch changed files = %+v, want none", edits)
	}
}

func TestApplyPatchChangedFilesRestoreFromTranscript(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	args := []byte(`{"patch":"*** Begin Patch\n*** Add File: docs/new.md\n+# New\n*** Delete File: tmp/old.txt\n*** End Patch"}`)
	m.rebuildSidebarFileEditsFromMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameApplyPatch, Args: args}}},
		{Role: "tool", ToolCallID: "call-1", ToolStatus: string(agent.ToolResultStatusSuccess)},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 2 {
		t.Fatalf("restored changed files = %d, want 2: %+v", len(edits), edits)
	}
	assertFileEdit(t, edits[0], "docs/new.md", 1, 0, false)
	assertFileEdit(t, edits[1], "tmp/old.txt", 0, 0, true)
}

func TestLegacyPatchChangedFilesRestoreFromTranscript(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	args := []byte(`{"path":"src/demo.go","patch":"@@\n-old\n+new"}`)
	m.rebuildSidebarFileEditsFromMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "patch", Args: args}}},
		{Role: "tool", ToolCallID: "call-1", ToolStatus: string(agent.ToolResultStatusSuccess), ToolDiff: "diff", ToolDiffAdded: 1, ToolDiffRemoved: 1},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 || edits[0].Path != "src/demo.go" {
		t.Fatalf("legacy patch restored edits = %+v, want src/demo.go", edits)
	}
}

func assertFileEdit(t *testing.T, got FileEdit, path string, added, removed int, deleted bool) {
	t.Helper()
	if got.Path != path || got.Added != added || got.Removed != removed || got.Deleted != deleted {
		t.Fatalf("file edit = %+v, want path=%q +%d -%d deleted=%t", got, path, added, removed, deleted)
	}
}
