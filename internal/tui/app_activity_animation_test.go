package tui

import (
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestActivityFrameAdvancesOnAnimTick(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.animTickGeneration = 1
	m.activitySpinnerFrameIndex = 0

	if got := m.activityFrame(); got != activeToolSpinnerSegments[0] {
		t.Fatalf("initial activityFrame = %q, want %q", got, activeToolSpinnerSegments[0])
	}

	for i := 1; i <= len(activeToolSpinnerSegments); i++ {
		_ = m.handleAnimTick(animTickMsg{generation: m.animTickGeneration, source: animTickSourceVisual})
		want := activeToolSpinnerSegments[i%len(activeToolSpinnerSegments)]
		if got := m.activityFrame(); got != want {
			t.Fatalf("frame after %d ticks = %q, want %q", i, got, want)
		}
	}

	// Ensure the tick-driven advancement is deterministic and does not depend on wall time.
	before := m.activityFrame()
	after := m.activityFrame()
	if after != before {
		t.Fatalf("activityFrame changed without tick: before=%q after=%q", before, after)
	}
}

func TestAnimTickSkipsStreamFlushWhenNoStreamingBlock(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	// Executing a tool/shell: spinner active, but no streaming text block.
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityExecuting, AgentID: "main"}
	m.animRunning = true
	m.animTickGeneration = 1
	m.displayState = stateForeground

	_ = m.handleAnimTick(animTickMsg{generation: m.animTickGeneration, source: animTickSourceVisual})
	if m.streamFlushScheduled {
		t.Fatal("stream flush should not be scheduled when there is no streaming content block")
	}
	// Spinner frame must still advance.
	if m.activitySpinnerFrameIndex != 1 {
		t.Fatalf("activitySpinnerFrameIndex = %d, want 1", m.activitySpinnerFrameIndex)
	}
}

func TestAnimTickSchedulesStreamFlushWhenStreamingBlockExists(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.activities["main"] = agent.AgentActivityEvent{Type: agent.ActivityStreaming, AgentID: "main"}
	m.animRunning = true
	m.animTickGeneration = 1
	m.displayState = stateForeground
	m.currentAssistantBlock = &Block{ID: 1, Type: BlockAssistant, Content: "streaming"}

	_ = m.handleAnimTick(animTickMsg{generation: m.animTickGeneration, source: animTickSourceVisual})
	if !m.streamFlushScheduled {
		t.Fatal("stream flush should be scheduled when a streaming content block exists")
	}
}
