package tui

import "testing"

func TestDropStartupDeferredTranscriptHiddenCachesReleasesOnlyOutsideHalo(t *testing.T) {
	const total = 60
	blocks := make([]*Block, total)
	meta := make([]startupDeferredBlockMeta, total)
	for i := range blocks {
		blocks[i] = &Block{ID: i + 1, Type: BlockAssistant, Content: "body"}
		blocks[i].mdCache = []string{"rendered"}
		meta[i] = startupDeferredBlockMeta{
			BlockID:    i + 1,
			Type:       BlockAssistant,
			block:      blocks[i],
			LineCounts: map[int]int{80: i + 1},
		}
	}
	m := &Model{startupDeferredTranscript: &startupDeferredTranscriptState{
		allBlocks:   blocks,
		blockMeta:   meta,
		windowStart: 20,
		windowEnd:   30,
	}}

	m.dropStartupDeferredTranscriptHiddenCaches()

	keepStart := 20 - startupDeferredTranscriptPreheatHalo
	keepEnd := 30 + startupDeferredTranscriptPreheatHalo
	for i, block := range blocks {
		inside := i >= keepStart && i < keepEnd
		if inside && block.mdCache == nil {
			t.Fatalf("block %d inside the window/halo lost its Markdown cache", i)
		}
		if !inside && block.mdCache != nil {
			t.Fatalf("block %d outside the window/halo kept its Markdown cache", i)
		}
		if meta[i].LineCounts[80] != i+1 {
			t.Fatalf("block %d line counts = %v, want them preserved", i, meta[i].LineCounts)
		}
		if block.Content == "" {
			t.Fatalf("block %d lost its content", i)
		}
	}
}

func TestResizeCacheSweepProtectsActiveArchiveBlocks(t *testing.T) {
	blocks := []*Block{
		{ID: 1, Type: BlockAssistant, Focused: true},
		{ID: 2, Type: BlockAssistant, Streaming: true},
		{ID: 3, Type: BlockUser, UserLocalShellPending: true},
		{ID: 4, Type: BlockToolCall, ResultDone: false},
		{ID: 5, Type: BlockAssistant},
	}
	for _, block := range blocks {
		block.mdCache = []string{"rendered"}
	}
	m := &Model{
		startupDeferredTranscript: &startupDeferredTranscriptState{
			allBlocks: blocks, windowStart: 30, windowEnd: 40,
		},
		resizeCacheSweepScheduled: true, resizeCacheSweepGeneration: 1,
	}
	m.handleResizeCacheSweep(resizeCacheSweepTickMsg{generation: 1})
	for _, block := range blocks[:4] {
		if block.mdCache == nil {
			t.Fatalf("protected archive block %d lost its cache", block.ID)
		}
	}
	if blocks[4].mdCache != nil {
		t.Fatal("inactive archive block retained its cache")
	}
}
