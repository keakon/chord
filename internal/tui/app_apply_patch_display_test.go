package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestPartiallyAppliedPatchTracksOnlyCommittedFileState(t *testing.T) {
	projectRoot := t.TempDir()
	m := NewModelWithSize(nil, 100, 30)
	m.workingDir = projectRoot
	m.sidebar.SetWorkingDir(projectRoot)
	m.sidebar.Update(nil, "main", "builder")
	args := `{"patch":"*** Begin Patch\n*** Update File: committed.go\n@@\n-old\n+new\n*** Update File: failed.go\n@@\n-missing\n+new\n*** End Patch"}`

	m.handleToolAgentEvent(agent.ToolCallStartEvent{ID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args})
	m.handleToolAgentEvent(agent.ToolResultEvent{
		CallID: "call-1", Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "apply_patch partially applied", Status: agent.ToolResultStatusError,
		FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{
			Path: filepath.Join(projectRoot, "committed.go"), Exists: true,
		}}, Changes: []message.ToolFileChange{{
			Path: filepath.Join(projectRoot, "committed.go"), Added: 3, Removed: 1,
		}}},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 {
		t.Fatalf("changed files = %d, want only committed.go: %+v", len(edits), edits)
	}
	assertFileEdit(t, edits[0], "committed.go", 3, 1, false)
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

func TestPartiallyAppliedPatchRestoresCommittedFileState(t *testing.T) {
	projectRoot := t.TempDir()
	m := NewModelWithSize(nil, 100, 30)
	m.workingDir = projectRoot
	m.sidebar.SetWorkingDir(projectRoot)
	m.sidebar.Update(nil, "main", "builder")
	args := []byte(`{"patch":"*** Begin Patch\n*** Update File: committed.go\n@@\n-old\n+new\n*** Update File: failed.go\n@@\n-missing\n+new\n*** End Patch"}`)
	m.rebuildSidebarFileEditsFromMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: tools.NameApplyPatch, Args: args}}},
		{
			Role: "tool", ToolCallID: "call-1", ToolStatus: string(agent.ToolResultStatusError),
			FileState: &message.ToolFileState{Writes: []message.TrackedFileState{{
				Path: filepath.Join(projectRoot, "committed.go"), Exists: true,
			}}, Changes: []message.ToolFileChange{{
				Path: filepath.Join(projectRoot, "committed.go"), Added: 2, Removed: 1,
			}}},
		},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 {
		t.Fatalf("restored changed files = %d, want only committed.go: %+v", len(edits), edits)
	}
	assertFileEdit(t, edits[0], "committed.go", 2, 1, false)
}

func TestLegacyPatchChangedFilesRestoreFromTranscript(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	args := []byte(`{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`)
	m.rebuildSidebarFileEditsFromMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "call-1", Name: "patch", Args: args}}},
		{Role: "tool", ToolCallID: "call-1", ToolStatus: string(agent.ToolResultStatusSuccess), ToolDiff: "diff", ToolDiffAdded: 1, ToolDiffRemoved: 1},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 || edits[0].Path != "src/demo.go" {
		t.Fatalf("legacy patch restored edits = %+v, want src/demo.go", edits)
	}
}

func TestElidedEditDiffRestoresSidebarFromCounts(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	m.rebuildSidebarFileEditsFromMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "edit-1", Name: tools.NameEdit, Args: []byte(`{"path":"src/demo.go","old_string":"old","new_string":"new"}`)}}},
		{Role: "tool", ToolCallID: "edit-1", ToolStatus: string(agent.ToolResultStatusSuccess), Content: message.FormatToolResultElided(601), ToolDiffAdded: 1, ToolDiffRemoved: 1},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 {
		t.Fatalf("restored elided edit files = %+v, want src/demo.go", edits)
	}
	assertFileEdit(t, edits[0], "src/demo.go", 1, 1, false)
}

func TestElidedEditDiffRestoresSidebarFromFileState(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	m.sidebar.Update(nil, "main", "builder")
	m.rebuildSidebarFileEditsFromMessages([]message.Message{
		{Role: "assistant", ToolCalls: []message.ToolCall{{ID: "edit-1", Name: tools.NameEdit, Args: []byte(`{"path":"src/demo.go","old_string":"old","new_string":"new"}`)}}},
		{Role: "tool", ToolCallID: "edit-1", ToolStatus: string(agent.ToolResultStatusSuccess), Content: message.FormatToolResultElided(601),
			FileState: &message.ToolFileState{Changes: []message.ToolFileChange{{Path: "src/demo.go", Added: 1, Removed: 1}}}},
	})

	edits := m.sidebar.CurrentAgentFiles()
	if len(edits) != 1 {
		t.Fatalf("restored elided edit files = %+v, want src/demo.go", edits)
	}
	assertFileEdit(t, edits[0], "src/demo.go", 1, 1, false)
}

func assertFileEdit(t *testing.T, got FileEdit, path string, added, removed int, deleted bool) {
	t.Helper()
	if got.Path != path || got.Added != added || got.Removed != removed || got.Deleted != deleted {
		t.Fatalf("file edit = %+v, want path=%q +%d -%d deleted=%t", got, path, added, removed, deleted)
	}
}

func TestApplyPatchStreamingCardShowsLivePatchPreview(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-stream-1"
	partial := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old`
	complete := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: partial,
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing streaming apply_patch block")
	}
	if block.Collapsed {
		t.Fatal("expected streaming apply_patch card to be expanded")
	}
	if block.ToolProgress != nil {
		t.Fatalf("expected no char-count progress, got %+v", *block.ToolProgress)
	}
	if block.Content != "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old" {
		t.Fatalf("Content = %q, want streamed patch text preview", block.Content)
	}
	lines := block.Render(100, "●")
	plain := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"*** Begin Patch", "*** Update File: src/demo.go", "-old"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected live patch preview to contain %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "+new") {
		t.Fatalf("did not expect unstreamed hunk line, got:\n%s", plain)
	}
	if strings.Contains(plain, "chars received") {
		t.Fatalf("expected no char count, got:\n%s", plain)
	}
	if strings.Contains(plain, `{"patch"`) {
		t.Fatalf("expected no raw JSON fragment, got:\n%s", plain)
	}
	deleted := renderedLineContaining(t, lines, "-old")
	assertRenderedTextBackground(t, deleted, "old", colorOfTheme(currentTheme.DiffDelLineBg))

	// The preview grows with the next delta (bypass the arg-render cadence).
	m.toolArgRenderState[callID] = toolArgRenderState{lastBytes: len(partial), lastAt: time.Now().Add(-200 * time.Millisecond)}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: complete,
	}})
	plain = stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "+new") {
		t.Fatalf("expected preview to grow with the new delta, got:\n%s", plain)
	}

	// Args complete: Content switches to the stable path display, and the
	// requested-patch preview takes over the body.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: complete, ArgsStreamingDone: true,
	}})
	if block.Content != `{"paths":["src/demo.go"]}` {
		t.Fatalf("Content after streaming done = %q, want stable path display", block.Content)
	}
	plain = stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "↳ Requested patch:") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected requested-patch preview after args complete, got:\n%s", plain)
	}

	// Result: the final diff replaces the preview.
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: callID, Name: tools.NameApplyPatch, ArgsJSON: complete,
		Result: "Applied patch", Status: agent.ToolResultStatusSuccess,
		Diff: "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
	}})
	plain = stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") || !strings.Contains(plain, "+new") {
		t.Fatalf("expected final diff after result, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Requested patch:") {
		t.Fatalf("expected final diff to replace the requested-patch preview, got:\n%s", plain)
	}
}

func TestApplyPatchStreamingCardStaysExpandedWhenExecutionQueued(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-queued-1"
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: args, ArgsStreamingDone: true,
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing apply_patch block")
	}
	if block.Collapsed {
		t.Fatal("expected streaming apply_patch card to stay expanded after args complete")
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallExecutionEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: args, State: agent.ToolCallExecutionStateQueued,
	}})
	if block.Collapsed {
		t.Fatal("expected queued apply_patch card to stay expanded")
	}
	plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "↳ Requested patch:") {
		t.Fatalf("expected queued apply_patch to keep the preview visible, got:\n%s", plain)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: callID, Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "Applied patch", Status: agent.ToolResultStatusSuccess,
		Diff: "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
	}})
	if block.Collapsed {
		t.Fatal("expected result apply_patch card to stay expanded")
	}
}

func TestApplyPatchStreamingCardFreeformInputTextPreview(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-freeform-1"

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "",
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing freeform apply_patch block")
	}
	if block.Collapsed {
		t.Fatal("expected freeform streaming apply_patch card to be expanded")
	}

	// Freeform deltas arrive through InputText with a canonical ArgsJSON
	// envelope; the preview must render the raw patch text verbatim. Bypass
	// the arg-render cadence (same pattern as the function-form streaming
	// test).
	delete(m.toolArgRenderState, callID)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "",
		ArgsJSON:  `{"patch":"*** Begin Patch\n"}`,
		InputText: "*** Begin Patch\n",
	}})
	if block.Content != "*** Begin Patch\n" {
		t.Fatalf("Content = %q, want raw freeform text", block.Content)
	}
	delete(m.toolArgRenderState, callID)
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "",
		ArgsJSON:  `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n"}`,
		InputText: "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n",
	}})
	plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	for _, want := range []string{"*** Begin Patch", "*** Update File: src/demo.go", "-old", "+new"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected freeform preview to contain %q, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, `{"patch"`) || strings.Contains(plain, "chars received") {
		t.Fatalf("expected raw text preview without envelope or char count, got:\n%s", plain)
	}
	deleted := renderedLineContaining(t, block.Render(100, "●"), "-old")
	assertRenderedTextBackground(t, deleted, "old", colorOfTheme(currentTheme.DiffDelLineBg))
}

func TestApplyPatchStreamingCardWithoutPatchTextStaysCompact(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-empty-1"

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: `{"`,
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing apply_patch block")
	}
	if block.Content != "" {
		t.Fatalf("Content = %q, want empty while no patch text has streamed", block.Content)
	}
	plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if strings.Contains(plain, `{"`) {
		t.Fatalf("expected no raw JSON fragment, got:\n%s", plain)
	}
	if strings.Contains(plain, "chars received") {
		t.Fatalf("expected no char count, got:\n%s", plain)
	}
}

// TestApplyPatchStreamingCardBypassesArgRenderCadence locks in live streaming:
// the apply_patch preview must update on every delta even when the generic
// arg-render cadence says "hold off", and the header must show the streamed
// target path while the patch is still arriving.
func TestApplyPatchStreamingCardBypassesArgRenderCadence(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-cadence-1"

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "",
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing freeform apply_patch block")
	}

	// A far-future lastAt would block a generic tool's streaming args; the
	// live patch preview must ignore it and still apply each delta.
	m.toolArgRenderState[callID] = toolArgRenderState{lastBytes: 0, lastAt: time.Now().Add(24 * time.Hour)}
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "",
		ArgsJSON:  `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old"}`,
		InputText: "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old",
	}})

	if block.Content != "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old" {
		t.Fatalf("Content = %q, want live raw preview despite cadence block", block.Content)
	}
	plain := stripANSI(strings.Join(block.Render(100, "●"), "\n"))
	if !strings.Contains(plain, "apply_patch src/demo.go") {
		t.Fatalf("expected streaming header to show the target path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "-old") {
		t.Fatalf("expected live preview body to render, got:\n%s", plain)
	}
}

// Measuring a card renders it in full, so invalidating it once per argument
// delta makes a streaming patch quadratic. The live preview coalesces by
// content instead: deltas inside the byte window leave the measured card
// alone, and the first delta past it refreshes the preview.
func TestApplyPatchStreamingCardCoalescesSubWindowDeltas(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-coalesce-1"

	head := "*** Begin Patch\n*** Update File: src/demo.go\n@@\n"
	// A mid-stream fragment: no closing quote or brace, so the card renders
	// the live patch text rather than a parsed argument summary.
	argsFor := func(body string) string {
		return `{"patch":"` + strings.ReplaceAll(head+body, "\n", `\n`)
	}

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "",
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing apply_patch block")
	}

	body := "+first line\n"
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: argsFor(body),
	}})
	settled := block.Content
	if !strings.Contains(settled, "+first line") {
		t.Fatalf("Content = %q, want the first delta applied", settled)
	}

	// Measure the card so a later re-measure is observable. UpdateBlock bumps
	// the render version exactly once per re-measure.
	_ = block.LineCount(100)
	measured := m.viewport.RenderVersion()

	// Deltas that stay inside the coalesce window must not re-measure.
	for i := range 4 {
		body += "+p" + string(rune('a'+i)) + "\n"
		_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
			ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: argsFor(body),
		}})
		if got := m.viewport.RenderVersion(); got != measured {
			t.Fatalf("delta %d inside the coalesce window re-measured the card (render version %d -> %d)", i, measured, got)
		}
		if block.Content != settled {
			t.Fatalf("delta %d inside the coalesce window changed Content to %q", i, block.Content)
		}
	}

	// Crossing the window refreshes the preview and re-measures once.
	body += "+" + strings.Repeat("x", patchPreviewCoalesceBytes) + "\n"
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallUpdateEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: argsFor(body),
	}})
	if got := m.viewport.RenderVersion(); got == measured {
		t.Fatal("delta past the coalesce window did not re-measure the card")
	}
	if !strings.Contains(block.Content, strings.Repeat("x", patchPreviewCoalesceBytes)) {
		t.Fatalf("Content = %q, want the delta past the coalesce window applied", block.Content)
	}
}

func TestApplyPatchCompletedCardShowsHeaderElapsedAndPath(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	callID := "call-patch-elapsed-1"
	args := `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`

	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolCallStartEvent{
		ID: callID, Name: tools.NameApplyPatch, AgentID: "", ArgsJSON: args,
	}})
	_ = m.handleAgentEvent(agentEventMsg{event: agent.ToolResultEvent{
		CallID: callID, Name: tools.NameApplyPatch, ArgsJSON: args,
		Result: "Applied patch", Status: agent.ToolResultStatusSuccess,
		Diff:     "--- src/demo.go\n+++ src/demo.go\n@@ -1 +1 @@\n-old\n+new\n",
		Duration: 2 * time.Second,
	}})
	block, ok := m.viewport.FindBlockByToolID(callID)
	if !ok {
		t.Fatal("missing apply_patch block")
	}
	lines := strings.Split(stripANSI(strings.Join(block.Render(100, ""), "\n")), "\n")
	header := ""
	for _, line := range lines {
		if strings.Contains(line, "apply_patch") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("expected header line, got:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(header, "apply_patch src/demo.go") {
		t.Fatalf("expected completed header to keep the target path, got:\n%s", header)
	}
	if !strings.Contains(header, "· ⏱ 2s") {
		t.Fatalf("expected completed header to carry the elapsed label, got:\n%s", header)
	}
	for _, line := range lines {
		if line != header && strings.Contains(line, "⏱") {
			t.Fatalf("expected elapsed only on the header, but found it on a body line:\n%s", line)
		}
	}
}

// TestFreeformApplyPatchErrorCardShowsPatchNotJSONEnvelope guards the error
// path for freeform custom-tool input: the card must render the patch text and
// the failure reason, never the raw {"patch":...} envelope.
func TestFreeformApplyPatchErrorCardShowsPatchNotJSONEnvelope(t *testing.T) {
	ApplyTheme(DefaultTheme())
	bare := "*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       bare,
		RawArgs:       `{"patch":"*** Begin Patch\n*** Update File: src/demo.go\n@@\n-old\n+new\n*** End Patch"}`,
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "hunk not found",
	}
	rendered := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(rendered, `{"patch"`) {
		t.Fatalf("expected error card to show freeform patch, not the JSON envelope, got:\n%s", rendered)
	}
	for _, want := range []string{"apply_patch src/demo.go", "*** Update File: src/demo.go", "+new", "hunk not found", "↳ Requested patch:"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected error card to contain %q, got:\n%s", want, rendered)
		}
	}
}
