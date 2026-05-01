package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func viewportRealTotal(v *Viewport) int {
	if v == nil {
		return 0
	}
	total := 0
	for _, b := range v.visibleBlocks() {
		total += v.blockSpanLines(b)
	}
	return total
}

func TestSpawnFinishedEventUpdatesExistingSpilledStatusBlockAndRecomputesTotalLines(t *testing.T) {
	m := NewModelWithSize(nil, 60, 8)
	m.viewport.maxHotBytes = 1024

	// Add enough content after the status card to force it off-screen and spilled.
	status := &Block{ID: 1, Type: BlockStatus, Content: "old", BackgroundObjectID: "job-7", AgentID: "builder-2"}
	m.viewport.AppendBlock(status)
	for i := 0; i < 8; i++ {
		m.viewport.AppendBlock(&Block{ID: 2 + i, Type: BlockAssistant, Content: strings.Repeat("tail ", 40)})
	}
	if !status.spillCold {
		t.Fatalf("precondition failed: expected status block to spill, got spillCold=%v", status.spillCold)
	}

	messages := []string{
		"[Job job-7 finished]\n\nDescription: Run backend tests with a much longer summary that wraps across multiple lines\nStatus: finished (exit 0)",
		"[Job job-7 finished]\n\nDescription: Run backend tests with a much longer summary that wraps across multiple lines and then appends extra details for diagnostics\nStatus: finished (exit 0)",
	}
	for _, msg := range messages {
		_ = m.handleAgentEvent(agentEventMsg{event: agent.SpawnFinishedEvent{
			BackgroundID: "job-7",
			AgentID:      "builder-2",
			Kind:         "job",
			Description:  "Run backend tests with a much longer summary that wraps across multiple lines",
			Status:       "finished (exit 0)",
			Message:      msg,
		}})
	}

	block, ok := m.viewport.FindStatusBlockByBackgroundObject("job-7")
	if !ok {
		t.Fatal("expected durable background status block to still exist")
	}
	if !strings.Contains(block.Content, "Run backend tests") {
		t.Fatalf("updated block content = %q, want backend tests", block.Content)
	}
	if block.spillCold {
		t.Fatal("updated spilled status block should materialize when mutated")
	}

	if got, want := m.viewport.TotalLines(), viewportRealTotal(m.viewport); got != want {
		t.Fatalf("viewport TotalLines() = %d, want %d after spilled status update", got, want)
	}
}
