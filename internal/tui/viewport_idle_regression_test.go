package tui

import "testing"

// Regression test for a long-session drift class:
// DropOffScreenCaches must account for turn-spacing lines when computing
// global line offsets, otherwise the viewport's cached block positions can
// gradually desync from the rendered transcript and selection hit-testing.
func TestViewportDropOffScreenCachesKeepsBlockStartsConsistent(t *testing.T) {
	v := NewViewport(40, 6)

	// Construct two blocks whose types can create turn boundaries.
	// (Even if turn boundaries are currently disabled, the invariant tested here
	// still holds: DropOffScreenCaches must not cause cached starts/spans to change
	// for a fixed transcript.)
	first := &Block{ID: 1, Type: BlockAssistant, Content: "assistant"}
	second := &Block{ID: 2, Type: BlockUser, Content: "user"}
	v.AppendBlock(first)
	v.AppendBlock(second)

	// Materialize caches.
	startsBefore := append([]int(nil), v.blockStarts()...)
	totalBefore := v.TotalLines()

	// Move viewport so the first block is offscreen, then drop caches.
	v.offset = max(0, totalBefore-v.height)
	v.clampOffset()
	v.DropOffScreenCaches()

	startsAfter := v.blockStarts()
	totalAfter := v.TotalLines()

	if totalAfter != totalBefore {
		t.Fatalf("TotalLines changed after DropOffScreenCaches: before=%d after=%d", totalBefore, totalAfter)
	}
	if len(startsAfter) != len(startsBefore) {
		t.Fatalf("blockStarts length changed after DropOffScreenCaches: before=%v after=%v", startsBefore, startsAfter)
	}
	for i := range startsBefore {
		if startsAfter[i] != startsBefore[i] {
			t.Fatalf("blockStarts changed after DropOffScreenCaches: before=%v after=%v", startsBefore, startsAfter)
		}
	}
}
