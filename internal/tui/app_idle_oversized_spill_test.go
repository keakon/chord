package tui

import (
	"strings"
	"testing"
)

func TestIdleOversizedSpillSkipsBlocksAboveBatchCeiling(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	t.Cleanup(func() { _ = m.viewport.Close() })
	m.viewport.SetSize(80, 10)

	huge := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: strings.Repeat("x", (8<<20)+1),
	}
	huge.lineCache = []string{"cached"}
	huge.lineCacheWidth = 80
	huge.lineCountCache = 1
	m.viewport.AppendBlock(huge)

	oversized := &Block{
		ID:      2,
		Type:    BlockAssistant,
		Content: strings.Repeat("x", (4<<20)+1024),
	}
	oversized.lineCache = []string{"cached"}
	oversized.lineCacheWidth = 80
	oversized.lineCountCache = 1
	m.viewport.AppendBlock(oversized)

	for i := range 10 {
		m.viewport.AppendBlock(&Block{ID: 10 + i, Type: BlockAssistant, Content: "small"})
	}
	m.viewport.ScrollToBottom()

	spilled, _ := m.viewport.SpillOffScreenOversizedBlocksBatch(2, idleOversizedSpillBatchBytes)
	if spilled != 1 {
		t.Fatalf("spilled %d blocks, want the single block within the ceiling", spilled)
	}
	if huge.spillCold {
		t.Fatal("idle oversized spill wrote a block above the batch ceiling")
	}
	if !oversized.spillCold {
		t.Fatal("idle oversized spill skipped a block within the batch ceiling")
	}
}

func TestIdleOversizedSpillSchedulerBatches(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	t.Cleanup(func() { _ = m.viewport.Close() })
	m.viewport.SetSize(80, 10)
	m.displayState = stateBackground

	for i := range 3 {
		block := &Block{
			ID:        i + 1,
			Type:      BlockAssistant,
			Content:   strings.Repeat("x", (4<<20)+1024),
			lineCache: []string{"cached"},
		}
		block.lineCacheWidth = 80
		block.lineCountCache = 1
		m.viewport.AppendBlock(block)
	}
	for i := range 10 {
		m.viewport.AppendBlock(&Block{ID: 10 + i, Type: BlockAssistant, Content: "small"})
	}
	m.viewport.ScrollToBottom()

	m.markIdleOversizedSpillPending()
	if cmd := m.maybeScheduleIdleOversizedSpill(); cmd == nil {
		t.Fatal("pending oversized spill should schedule a tick")
	}
	if !m.idleOversizedSpillScheduled {
		t.Fatal("scheduled flag not set")
	}
	firstGen := m.idleOversizedSpillGeneration
	next := m.handleIdleOversizedSpillTick(idleOversizedSpillTickMsg{generation: firstGen})
	if next == nil {
		t.Fatal("first batch should leave more work scheduled")
	}
	spilled := 0
	for _, block := range m.viewport.blocks[:3] {
		if block.spillCold {
			spilled++
		}
	}
	if spilled != idleOversizedSpillBatchBlocks {
		t.Fatalf("first batch spilled %d blocks, want %d", spilled, idleOversizedSpillBatchBlocks)
	}
	if !m.idleOversizedSpillPending {
		t.Fatal("remaining oversized blocks should keep the request pending")
	}

	secondGen := m.idleOversizedSpillGeneration
	if secondGen == firstGen {
		t.Fatal("second batch generation did not advance")
	}
	if next := m.handleIdleOversizedSpillTick(idleOversizedSpillTickMsg{generation: secondGen}); next != nil {
		t.Fatal("last batch should not schedule another tick")
	}
	for _, block := range m.viewport.blocks[:3] {
		if !block.spillCold {
			t.Fatalf("block %d stayed hot after the final batch", block.ID)
		}
	}
	if m.idleOversizedSpillPending || m.idleOversizedSpillScheduled {
		t.Fatal("completed oversized spill left scheduler state behind")
	}
}
