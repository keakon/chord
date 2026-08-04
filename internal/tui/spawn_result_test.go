package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestSpawnFinishedEventAppendsDurableStatusBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SpawnFinishedEvent{
		BackgroundID: "job-1",
		AgentID:      "main-1",
		Kind:         "job",
		Description:  "Run production build",
		Status:       "finished (exit 0)",
		Message:      "[Job job-1 finished]\n\nDescription: Run production build\nStatus: finished (exit 0)",
	}})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-1")
	if !ok {
		t.Fatal("expected durable status block for background object result")
	}
	if block.AgentID != "main-1" {
		t.Fatalf("block.AgentID = %q, want main-1", block.AgentID)
	}
	if !strings.Contains(block.Content, "Run production build") {
		t.Fatalf("block.Content = %q, want build description", block.Content)
	}
}

func TestSpawnFinishedEventUpdatesExistingDurableStatusBlock(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockStatus, Content: "old", BackgroundObjectID: "job-7", AgentID: "builder-2"})

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SpawnFinishedEvent{
		BackgroundID: "job-7",
		AgentID:      "builder-2",
		Kind:         "job",
		Description:  "Run backend tests",
		Status:       "finished (exit 0)",
		Message:      "[Job job-7 finished]\n\nDescription: Run backend tests\nStatus: finished (exit 0)",
	}})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-7")
	if !ok {
		t.Fatal("expected durable background status block to still exist")
	}
	if !strings.Contains(block.Content, "Run backend tests") {
		t.Fatalf("updated block content = %q, want backend tests", block.Content)
	}
}

func TestSpawnFinishedEventForMainAgentVisibleInMainView(t *testing.T) {
	m := NewModelWithSize(nil, 120, 30)
	m.viewport.SetFilter("main")

	_ = m.handleAgentEvent(agentEventMsg{event: agent.SpawnFinishedEvent{
		BackgroundID: "job-3",
		AgentID:      "main",
		Kind:         "job",
		Description:  "Run integration tests",
		Status:       "finished (exit 0)",
		Message:      "[Job job-3 finished]\n\nDescription: Run integration tests\nStatus: finished (exit 0)",
	}})

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-3")
	if !ok {
		t.Fatal("expected durable status block for main-agent background result")
	}
	if block.AgentID != "" {
		t.Fatalf("block.AgentID = %q, want empty main attribution", block.AgentID)
	}
	visible := false
	for _, b := range m.viewport.visibleBlocks() {
		if b != nil && b.BackgroundObjectID == "job-3" {
			visible = true
			break
		}
	}
	if !visible {
		t.Fatal("expected background result block to be visible under the main filter")
	}
}

func TestFilterBlocksByAgentMainIncludesMainAttributedBlocks(t *testing.T) {
	blocks := []*Block{
		{ID: 1, AgentID: ""},
		{ID: 2, AgentID: "main"},
		{ID: 3, AgentID: "builder-2"},
	}
	filtered := filterBlocksByAgent(blocks, "main")
	if len(filtered) != 2 {
		t.Fatalf("len(filtered) = %d, want 2", len(filtered))
	}
	if filtered[0].ID != 1 || filtered[1].ID != 2 {
		t.Fatalf("filtered IDs = [%d %d], want [1 2]", filtered[0].ID, filtered[1].ID)
	}
}
