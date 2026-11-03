package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/tui/syntax"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

// A block above the spill-exemption threshold still releases its derived
// render caches: the exemption only skips the spill-store round-trip.
func TestEnforceHotBudgetReleasesLargeBlockRenderCachesWithoutSpill(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(40, 6)
	defer func() { _ = v.Close() }()
	v.maxHotBytes = 1 << 10

	// UserLocalShellResult counts as hot payload but is not rendered while
	// UserLocalShellCmd is empty, so the fixture stays cheap to render.
	big := &Block{
		ID:                   1,
		Type:                 BlockUser,
		Content:              "big",
		UserLocalShellResult: strings.Repeat("x", (4<<20)+1),
	}
	v.AppendBlock(big)
	big.mdCache = []string{"rendered body"}
	big.codeHL = syntax.New("", "", "go")
	_ = big.codeHL.Line("var x = 1", "")
	if big.lineCache == nil {
		t.Fatal("fixture: expected the appended block to have a rendered line cache")
	}

	v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: strings.Repeat("tail line\n", 12)})

	if big.spillCold {
		t.Fatal("spill-exempt block must stay hot")
	}
	if big.Content != "big" {
		t.Fatalf("cache release dropped the block content: %q", big.Content)
	}
	if big.mdCache != nil || big.lineCache != nil || big.codeHL != nil {
		t.Fatal("spill-exempt block kept its derived render caches")
	}
}

// Cache release does not depend on the spill store, so a viewport without one
// still reclaims derived render caches above budget.
func TestEnforceHotBudgetReleasesCachesWithoutSpillStore(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(40, 6)
	if err := v.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if v.spill != nil {
		t.Fatal("fixture: expected the spill store to be detached")
	}
	v.maxHotBytes = 1 << 10

	big := &Block{
		ID:                   1,
		Type:                 BlockUser,
		Content:              "big",
		UserLocalShellResult: strings.Repeat("x", (4<<20)+1),
	}
	v.AppendBlock(big)
	big.mdCache = []string{"rendered body"}

	v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: strings.Repeat("tail line\n", 12)})

	if big.spillCold {
		t.Fatal("block must stay hot without a spill store")
	}
	if big.mdCache != nil || big.lineCache != nil {
		t.Fatal("derived render caches were not released without a spill store")
	}
	if big.Content != "big" {
		t.Fatalf("cache release dropped the block content: %q", big.Content)
	}
}

// Releasing caches is the first level of enforcement: when that brings the
// viewport back under budget, no block is spilled to disk.
func TestEnforceHotBudgetReleasesRenderCachesBeforeSpilling(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(40, 6)
	defer func() { _ = v.Close() }()

	block := &Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("body line\n", 200)}
	v.AppendBlock(block)
	v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: strings.Repeat("tail line\n", 12)})
	if block.lineCache == nil {
		t.Fatal("fixture: expected a rendered line cache")
	}

	v.maxHotBytes = block.estimatedHotBytes() - 1
	v.markHotBudgetNeedsEnforcement()
	v.enforceHotBudget()

	if block.spillCold {
		t.Fatal("cache release alone should have kept the block out of the spill store")
	}
	if block.lineCache != nil || block.mdCache != nil {
		t.Fatal("off-screen block kept its derived render caches")
	}
	if block.Content == "" {
		t.Fatal("cache release dropped the block content")
	}
}

func TestEstimatedHotBytesIncludesSyntaxHighlighterCaches(t *testing.T) {
	// Content is above the 512-byte estimate floor so the highlighter's
	// contribution cannot be swallowed by it.
	block := &Block{Type: BlockAssistant, Content: strings.Repeat("body text ", 200)}
	block.hotBytesMemoValid = false
	base := block.estimatedHotBytes()

	block.codeHL = syntax.New("", "", "go")
	_ = block.codeHL.Line("var x = 1", "")
	block.hotBytesMemoValid = false
	withHighlighter := block.estimatedHotBytes()

	if got := block.codeHL.RenderCacheBytes(); got <= 0 {
		t.Fatalf("RenderCacheBytes() = %d, want > 0", got)
	}
	if withHighlighter <= base {
		t.Fatalf("estimatedHotBytes with highlighter = %d, without = %d; expected the cache to contribute", withHighlighter, base)
	}
}

func TestSpillBlockClearsAllSyntaxHighlighterCaches(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(80, 6)
	defer func() { _ = v.Close() }()

	block := &Block{ID: 1, Type: BlockAssistant, Content: "body"}
	v.AppendBlock(block)
	block.codeHL = syntax.New("", "", "go")
	block.previewHL = syntax.New("", "", "go")
	block.richMarkdownHL = syntax.New("", "", "go")
	block.compactionSectionHL = []*syntax.Highlighter{syntax.New("", "", "go")}

	if !v.spillBlock(block) {
		t.Fatal("expected spillBlock to succeed")
	}
	if block.codeHL != nil || block.previewHL != nil || block.richMarkdownHL != nil || block.compactionSectionHL != nil {
		t.Fatal("spill left syntax highlighter caches resident")
	}
}

// The cached-only enforcement path must not spill blocks whose content is
// still being produced. A focused card is different: the quiescent paths may
// spill it and hydrate on the next interaction, which the copy flow relies on.
func TestEnforceHotBudgetCachedOnlySkipsStreamingAndPendingBlocks(t *testing.T) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(40, 6)
	defer func() { _ = v.Close() }()

	inactive := &Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("inactive body ", 60)}
	streaming := &Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("stream body ", 60), Streaming: true}
	focused := &Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("focus body ", 60), Focused: true}
	pending := &Block{ID: 4, Type: BlockToolCall, ToolName: "read", Content: `{"path":"a.go"}`}
	tail := &Block{ID: 5, Type: BlockUser, Content: strings.Repeat("tail line\n", 12)}
	v.AppendBlocks([]*Block{inactive, streaming, focused, pending, tail})

	// A zero-ish budget forces both spillable candidates to go cold, so the
	// assertions below do not depend on render-cache sizes.
	v.maxHotBytes = 1
	v.markHotBudgetNeedsEnforcement()
	v.enforceHotBudgetCachedOnly()

	if !inactive.spillCold {
		t.Fatal("expected the inactive off-screen block to be reclaimed")
	}
	if !focused.spillCold {
		t.Fatal("quiescent enforcement must be allowed to spill a focused card")
	}
	for _, block := range []*Block{streaming, pending} {
		if block.spillCold {
			t.Fatalf("block %d with in-flight content was spilled", block.ID)
		}
		if block.lineCache == nil {
			t.Fatalf("block %d with in-flight content lost its render cache", block.ID)
		}
	}
}

// Releasing the render caches keeps the line-count metadata, so offset
// recomputation (AppendBlock, resize) must not re-render the block. Without
// this, dropping caches turns transcript construction into repeated full
// renders of every released block.
func TestLineCountUsesPreservedCountAfterCacheRelease(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{Type: BlockAssistant, Content: strings.Repeat("body line\n", 40)}
	want := block.LineCount(80)
	if block.lineCache == nil {
		t.Fatal("fixture: expected LineCount to render the block")
	}

	block.ReleaseDerivedRenderCaches()
	if block.lineCache != nil {
		t.Fatal("fixture: expected the release to drop the rendered lines")
	}

	if got := block.LineCount(80); got != want {
		t.Fatalf("LineCount after cache release = %d, want %d", got, want)
	}
	if block.lineCache != nil {
		t.Fatal("LineCount after cache release re-rendered the block")
	}

	// A different width still needs a fresh render.
	narrow := block.LineCount(40)
	if narrow <= want {
		t.Fatalf("fixture: expected a narrower width to need more lines, got %d want more than %d", narrow, want)
	}
	if block.lineCache == nil {
		t.Fatal("LineCount must re-render for a new width")
	}
}
