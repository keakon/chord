package tui

import (
	"strings"
	"testing"
)

// Regression test for a long-session drift class:
// DropOffScreenCaches must keep the viewport's cached block positions
// consistent, otherwise the cached starts/spans can gradually desync from the
// rendered transcript and selection hit-testing. The turn-spacing mechanism
// that originally motivated this test was removed, but the invariant still
// holds: DropOffScreenCaches must not change cached starts/spans for a fixed
// transcript.
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

func TestViewportDropOffScreenCachesReleasesDerivedCaches(t *testing.T) {
	v := NewViewport(40, 6)
	visible := &Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("visible body line\n", 20)}
	offscreen := &Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("offscreen body line\n", 8)}
	v.AppendBlock(visible)
	v.AppendBlock(offscreen)
	// Appending sticks the viewport to the newest block; scroll back to the top
	// so the first block is the visible one.
	v.sticky = false
	v.offset = 0
	v.clampOffset()

	starts := v.blockStarts()
	if len(starts) != 2 || starts[1] <= v.height+5 {
		t.Fatalf("fixture: blockStarts = %v, want block 2 to start past the preserved window", starts)
	}

	visible.mdCache = []string{"kept"}
	offscreen.mdCache = []string{"dropped"}
	offscreen.codeHL = newCodeHighlighterWithLanguage("", "", "go")

	v.DropOffScreenCaches()

	if visible.mdCache == nil {
		t.Fatal("visible block lost its Markdown cache")
	}
	if offscreen.mdCache != nil {
		t.Fatalf("off-screen block kept its Markdown cache: %#v", offscreen.mdCache)
	}
	if offscreen.codeHL != nil {
		t.Fatal("off-screen block kept its syntax highlighter")
	}
	if offscreen.Content == "" {
		t.Fatal("release must keep the block content")
	}
}

func TestBlockReleaseDerivedRenderCachesKeepsContentAndLineMetadata(t *testing.T) {
	block := &Block{ID: 1, Type: BlockAssistant, Content: "body **text**"}
	block.mdCache = []string{"rendered"}
	block.mdCacheWidth = 80
	block.mdCacheContent = "body **text**"
	block.mdCacheThemeVersion = 3
	block.viewportCache = []string{"styled"}
	block.viewportCacheWidth = 80
	block.lineCache = []string{"line"}
	block.lineCacheWidth = 80
	block.lineCountCache = 1
	block.searchTextLower = "body text"
	block.searchTextReady = true
	block.spillLineCounts = map[int]int{80: 1}
	block.thinkingStreamSettled = []thinkingStreamSettledCache{{raw: "settled"}}

	block.ReleaseDerivedRenderCaches()

	if block.Content != "body **text**" {
		t.Fatalf("Content = %q, want it preserved", block.Content)
	}
	if block.mdCache != nil || block.mdCacheWidth != 0 || block.mdCacheContent != "" || block.mdCacheThemeVersion != 0 {
		t.Fatalf("Markdown cache survived release: %#v", block.mdCache)
	}
	if block.viewportCache != nil || block.viewportCacheWidth != 0 {
		t.Fatal("viewport cache survived release")
	}
	if block.lineCache != nil || block.lineCountCache != 0 {
		t.Fatal("rendered line cache survived release")
	}
	if block.searchTextLower != "" || block.searchTextReady {
		t.Fatal("search cache survived release")
	}
	if block.thinkingStreamSettled != nil {
		t.Fatal("thinking settled cache survived release")
	}
	if len(block.spillLineCounts) == 0 {
		t.Fatal("spill line counts must survive release")
	}
}

func TestBlockReleaseDerivedRenderCachesSkipsStreamingBlock(t *testing.T) {
	block := &Block{ID: 1, Type: BlockAssistant, Content: "partial", Streaming: true}
	block.mdCache = []string{"rendered"}

	block.ReleaseDerivedRenderCaches()

	if block.mdCache == nil {
		t.Fatal("streaming block must keep its settled Markdown cache")
	}
}
