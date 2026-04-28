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
