package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

func TestSidebarAddFileEditUsesExplicitStats(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	sidebar.AddFileEdit("", "/tmp/main.go", 2, 1)

	if got := len(sidebar.agents); got != 1 {
		t.Fatalf("sidebar entries = %d, want 1", got)
	}
	edits := sidebar.agents[0].EditedFiles
	if got := len(edits); got != 1 {
		t.Fatalf("edited files = %d, want 1", got)
	}
	if edits[0].Path != "/tmp/main.go" {
		t.Fatalf("path = %q, want /tmp/main.go", edits[0].Path)
	}
	if edits[0].Added != 2 || edits[0].Removed != 1 {
		t.Fatalf("diff stats = +%d -%d, want +2 -1", edits[0].Added, edits[0].Removed)
	}
}

func TestSidebarAddFileDeleteMarksDeletedWithoutFakeLineCount(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	sidebar.AddFileDelete("main", "/tmp/obsolete.go")

	edits := sidebar.agents[0].EditedFiles
	if len(edits) != 1 {
		t.Fatalf("changed files = %d, want 1", len(edits))
	}
	if !edits[0].Deleted {
		t.Fatalf("deleted flag = false, want true: %+v", edits[0])
	}
	if edits[0].Added != 0 || edits[0].Removed != 0 {
		t.Fatalf("deleted file diff stats = +%d -%d, want +0 -0", edits[0].Added, edits[0].Removed)
	}
}

func TestSidebarAddFileEditAfterDeleteClearsDeletedState(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	sidebar.AddFileDelete("main", "/tmp/obsolete.go")
	sidebar.AddFileEdit("main", "/tmp/obsolete.go", 3, 0)

	edits := sidebar.agents[0].EditedFiles
	if len(edits) != 1 {
		t.Fatalf("changed files = %d, want 1", len(edits))
	}
	if edits[0].Deleted {
		t.Fatalf("deleted flag should clear after later edit: %+v", edits[0])
	}
	if edits[0].Added != 3 || edits[0].Removed != 0 {
		t.Fatalf("diff stats = +%d -%d, want +3 -0", edits[0].Added, edits[0].Removed)
	}
}

func TestSidebarAddFileEditAccumulatesCounts(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	sidebar.AddFileEdit("main", "/tmp/main.go", 2, 1)
	sidebar.AddFileEdit("main", "/tmp/main.go", 3, 4)

	edits := sidebar.agents[0].EditedFiles
	if len(edits) != 1 {
		t.Fatalf("edited files = %d, want 1", len(edits))
	}
	if edits[0].Added != 5 || edits[0].Removed != 5 {
		t.Fatalf("accumulated diff stats = +%d -%d, want +5 -5", edits[0].Added, edits[0].Removed)
	}
}

func TestSidebarAddFileEditSkipsEmptyFiles(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	sidebar.AddFileEdit("main", "/tmp/empty.go", 0, 0)

	if got := len(sidebar.agents[0].EditedFiles); got != 0 {
		t.Fatalf("edited files = %d, want 0 for empty file", got)
	}
}

func TestSidebarUpdatePreservesEditedFiles(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	subAgents := []agent.SubAgentInfo{{
		InstanceID: "agent-1",
		TaskDesc:   "task",
	}}

	sidebar.Update(subAgents, "main", "builder")
	sidebar.AddFileEdit("main", "/tmp/main.go", 1, 0)
	sidebar.AddFileEdit("agent-1", "/tmp/sub.go", 1, 0)

	sidebar.Update(subAgents, "main", "builder")

	if got := len(sidebar.agents[0].EditedFiles); got != 1 {
		t.Fatalf("main edited files after refresh = %d, want 1", got)
	}
	if got := len(sidebar.agents[1].EditedFiles); got != 1 {
		t.Fatalf("sub-agent edited files after refresh = %d, want 1", got)
	}
}

func TestShouldTrackSidebarFileEdit(t *testing.T) {
	if !shouldTrackSidebarFileEdit(tools.NameWrite) {
		t.Fatal("write should be tracked as a sidebar file edit")
	}
	if !shouldTrackSidebarFileEdit(tools.NameEdit) {
		t.Fatal("edit should be tracked as a sidebar file edit")
	}
	if !shouldTrackSidebarFileEdit(tools.NameDelete) {
		t.Fatal("delete should be tracked as a sidebar file edit")
	}
	if shouldTrackSidebarFileEdit(tools.NameRead) {
		t.Fatal("read should not be tracked as a sidebar file edit")
	}
}

func TestSidebarEntryDisplayName_UsesUnifiedTaskSummaryPolicy(t *testing.T) {
	if got := sidebarEntryDisplayName(SidebarEntry{ID: "main", TaskDesc: "builder", AgentDefName: "should-not-win"}); got != "builder" {
		t.Fatalf("main display name = %q, want %q", got, "builder")
	}
	if got := sidebarEntryDisplayName(SidebarEntry{ID: "agent-1", AgentDefName: "coder", TaskDesc: "update prompt tests"}); got != "update prompt tests" {
		t.Fatalf("subagent display name = %q, want task desc", got)
	}
	if got := sidebarEntryDisplayName(SidebarEntry{ID: "agent-2", AgentDefName: "reviewer"}); got != "reviewer" {
		t.Fatalf("subagent fallback name = %q, want %q", got, "reviewer")
	}
	if got := sidebarEntryDisplayName(SidebarEntry{ID: "agent-3"}); got != "agent-3" {
		t.Fatalf("subagent id fallback = %q, want %q", got, "agent-3")
	}
}

func TestSidebarBuildLines_UsesTaskDescForSubagents(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "update prompt tests", AgentDefName: "coder"}}, "main", "builder")

	lines := sidebar.buildLines(40)
	plain := strings.Join([]string{stripANSI(lines[0]), stripANSI(lines[1])}, "\n")
	if !strings.Contains(plain, "builder") {
		t.Fatalf("sidebar should show main role, got %q", plain)
	}
	if !strings.Contains(plain, "update prompt tests") {
		t.Fatalf("sidebar should show subagent task description, got %q", plain)
	}
	if strings.Contains(plain, "coder") {
		t.Fatalf("sidebar should prefer task description over agent short name, got %q", plain)
	}
}

func TestSidebarBuildLinesDeletedFileUsesStrikethroughWithoutStats(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")
	sidebar.AddFileDelete("main", "/tmp/obsolete.go")

	lines := sidebar.buildLines(40)
	joined := strings.Join(lines, "\n")
	plain := stripANSI(joined)
	if !strings.Contains(plain, "obsolete.go") {
		t.Fatalf("deleted file missing from sidebar lines: %q", plain)
	}
	if strings.Contains(plain, "-1") || strings.Contains(plain, "+0") {
		t.Fatalf("deleted file should not show fake line stats, got %q", plain)
	}
	if !strings.Contains(joined, "\x1b[9m") && !strings.Contains(joined, ";9m") {
		t.Fatalf("deleted file should render with strikethrough, got %q", joined)
	}
}

func TestSidebarBuildLinesPrioritizesFileStatsWhenNarrow(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")
	sidebar.AddFileEdit("main", "/tmp/README_CN.md", 14, 4)

	const width = 12
	lines := sidebar.buildLines(width)
	if len(lines) < 2 {
		t.Fatalf("sidebar lines = %#v, want main row + file row", lines)
	}
	fileLine := stripANSI(lines[1])
	if !strings.Contains(fileLine, "+14 -4") {
		t.Fatalf("narrow file row should keep full stats, got %q", fileLine)
	}
	if got := tuiStringWidth(fileLine); got > width {
		t.Fatalf("narrow file row width = %d, want <= %d: %q", got, width, fileLine)
	}
	if !strings.HasPrefix(fileLine, "  REA ") {
		t.Fatalf("narrow file row should truncate filename before stats, got %q", fileLine)
	}
}

func TestSidebarPendingTaskUsesIconOnlyPlaceholder(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")
	sidebar.AddPendingTask()

	lines := sidebar.buildLines(12)
	if len(lines) < 2 {
		t.Fatalf("sidebar lines = %#v, want main row + pending placeholder", lines)
	}
	got := strings.TrimSpace(stripANSI(lines[len(lines)-1]))
	if got != "◌" {
		t.Fatalf("pending placeholder = %q, want %q", got, "◌")
	}
	if strings.Contains(stripANSI(lines[len(lines)-1]), "launching") {
		t.Fatalf("pending placeholder should not show launching text, got %q", stripANSI(lines[len(lines)-1]))
	}
}

func TestSidebarAgentsSummaryCountsPendingPlaceholderInTotal(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")
	sidebar.AddPendingTask()

	if got := sidebar.AgentsSummary(); got != "0/1" {
		t.Fatalf("AgentsSummary() = %q, want %q", got, "0/1")
	}
}

func TestSidebarCompletedStatusUsesDoneSemantics(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update([]agent.SubAgentInfo{{InstanceID: "agent-1", TaskDesc: "ship tests"}}, "main", "builder")
	sidebar.UpdateStatus("agent-1", "completed")

	if got := sidebar.AgentsSummary(); got != "1/1" {
		t.Fatalf("AgentsSummary() = %q, want %q", got, "1/1")
	}
	lines := sidebar.buildLines(32)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(stripANSI(joined), "✓ ship tests") {
		t.Fatalf("sidebar should render completed as done semantics, got:\n%s", stripANSI(joined))
	}
}

func TestSidebarNormalizesFilePathsForDuplicateDetection(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	// Add the same file with different path representations
	sidebar.AddFileEdit("main", "test.go", 5, 2)
	sidebar.AddFileEdit("main", "./test.go", 3, 1)
	sidebar.AddFileEdit("main", "./././test.go", 2, 0)

	edits := sidebar.agents[0].EditedFiles
	if got := len(edits); got != 1 {
		t.Fatalf("edited files = %d, want 1 (paths should be normalized and merged)", got)
	}
	if edits[0].Path != "test.go" {
		t.Fatalf("normalized path = %q, want %q", edits[0].Path, "test.go")
	}
	// Stats should be accumulated
	if edits[0].Added != 10 || edits[0].Removed != 3 {
		t.Fatalf("accumulated diff stats = +%d -%d, want +10 -3", edits[0].Added, edits[0].Removed)
	}
}

func TestSidebarNormalizesDeletePathToMatchEditPath(t *testing.T) {
	sidebar := NewSidebar(DefaultTheme())
	sidebar.Update(nil, "main", "builder")

	// Edit with one path form
	sidebar.AddFileEdit("main", "./src/main.go", 5, 2)
	// Delete with a different but equivalent path form
	sidebar.AddFileDelete("main", "src/main.go")

	edits := sidebar.agents[0].EditedFiles
	if got := len(edits); got != 1 {
		t.Fatalf("edited files = %d, want 1 (paths should be normalized)", got)
	}
	if !edits[0].Deleted {
		t.Fatalf("deleted flag = false, want true")
	}
	// Stats from edit should be preserved
	if edits[0].Added != 5 || edits[0].Removed != 2 {
		t.Fatalf("diff stats = +%d -%d, want +5 -2", edits[0].Added, edits[0].Removed)
	}
}
