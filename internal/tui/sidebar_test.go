package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
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
	if !shouldTrackSidebarFileEdit("Write") {
		t.Fatal("Write should be tracked as a sidebar file edit")
	}
	if !shouldTrackSidebarFileEdit("Edit") {
		t.Fatal("Edit should be tracked as a sidebar file edit")
	}
	if !shouldTrackSidebarFileEdit("Delete") {
		t.Fatal("Delete should be tracked as a sidebar file edit")
	}
	if shouldTrackSidebarFileEdit("Read") {
		t.Fatal("Read should not be tracked as a sidebar file edit")
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
