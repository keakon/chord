package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestPackedRenderCachePreservesCardAndBoundsDecodedWindow(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := strings.Repeat("Paragraph with **bold**, `inline code`, and a [link](https://example.invalid).\n\n", 600)
	for _, kind := range []BlockType{BlockAssistant, BlockThinking} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			block := &Block{ID: 7, Type: kind, Content: content}
			want := block.Render(100, "")
			if got := block.LineCount(100); got != len(want) {
				t.Fatalf("line count = %d, want %d", got, len(want))
			}
			if block.packedRender == nil || block.lineCache != nil || block.mdCache != nil {
				t.Fatal("large card did not release expanded caches")
			}
			for _, start := range []int{0, 120, 256, len(want) - 40, 512, 0} {
				end := min(start+40, len(want))
				got := block.RenderRange(100, "", start, end)
				if !slices.Equal(got, want[start:end]) {
					t.Fatalf("range [%d,%d) differs from full render", start, end)
				}
			}
			rawBytes := 0
			for _, line := range want {
				rawBytes += len(line)
			}
			if bytes := block.packedRender.bytes(); bytes >= rawBytes/2 {
				t.Fatalf("cache holds %d bytes for %d rendered bytes", bytes, rawBytes)
			}
			if got := block.Render(100, ""); !slices.Equal(got, want) {
				t.Fatal("full render changed after packing")
			}
			if clone := cloneBlockForDeferredSource(block); clone.packedRender != nil {
				t.Fatal("deferred clone shares mutable decoded cache")
			}
			for _, width := range []int{80, 120} {
				fresh := &Block{ID: 7, Type: kind, Content: content}
				expected := fresh.Render(width, "")
				if got := block.RenderRange(width, "", 100, 140); !slices.Equal(got, expected[100:140]) {
					t.Fatal("resized range differs from fresh render")
				}
			}
			block.ReleaseDerivedRenderCaches()
			if block.packedRender != nil {
				t.Fatal("release retained packed render")
			}
		})
	}
}

func TestPackedRenderCacheInvalidatesContentAndFocus(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := strings.Repeat("A paragraph with **emphasis** and ordinary words.\n\n", 500)
	block := &Block{ID: 3, Type: BlockAssistant, Content: content}
	_ = block.LineCount(100)
	block.Content = "Replacement."
	if got, want := block.Render(100, ""), (&Block{ID: 3, Type: BlockAssistant, Content: "Replacement."}).Render(100, ""); !slices.Equal(got, want) {
		t.Fatal("content replacement reused stale packed rows")
	}
	block.Content = content
	block.InvalidateCache()
	_ = block.LineCount(100)
	block.Focused = true
	fresh := &Block{ID: 3, Type: BlockAssistant, Content: content, Focused: true}
	if got, want := block.RenderRange(100, "", 0, 40), fresh.Render(100, "")[:40]; !slices.Equal(got, want) {
		t.Fatal("focus change reused stale rail")
	}
}

func TestPackedRenderCacheSelectionSearchThemeAndSpill(t *testing.T) {
	ApplyTheme(uitheme.Default())
	t.Cleanup(func() { ApplyTheme(uitheme.Default()) })
	block := &Block{ID: 3, Type: BlockAssistant, Content: strings.Repeat("A paragraph with **emphasis** and ordinary words.\n\n", 500) + "Unique target.\n"}
	want := block.Render(100, "")
	_ = block.LineCount(100)
	if block.packedRender == nil {
		t.Fatal("expected packed rows")
	}
	reference := *block
	reference.packedRender = nil
	reference.lineCache = want
	referenceViewport := &Viewport{width: 100, blocks: []*Block{&reference}}
	viewport := &Viewport{width: 100, blocks: []*Block{block}}
	for _, line := range []int{0, 127, 128, 900} {
		gotText, gotWidth := viewport.GetLinePlain(block.ID, line)
		wantText, wantWidth := referenceViewport.GetLinePlain(block.ID, line)
		if gotText != wantText || gotWidth != wantWidth {
			t.Fatalf("line %d differs", line)
		}
	}
	for _, selection := range []SelectionRange{
		{StartBlockID: 3, StartLine: 120, StartCol: 7, EndBlockID: 3, EndLine: 300, EndCol: 31},
		{StartBlockID: 3, StartLine: 0, EndBlockID: 3, EndLine: len(want) - 1, EndCol: 100},
	} {
		if got, expected := viewport.ExtractSelectionText(selection), referenceViewport.ExtractSelectionText(selection); got != expected {
			t.Fatal("selection differs from expanded rows")
		}
	}
	expected, expectedFound := searchMatchLineOffsetInRenderedLines(want, "unique target", 100)
	if got, found := block.searchMatchLineOffsetInCachedRender("unique target", 100); got != expected || found != expectedFound || !found {
		t.Fatalf("search = %d, %v; want %d, %v", got, found, expected, expectedFound)
	}
	if block.lineCache != nil || block.viewportCache != nil {
		t.Fatal("interaction retained a fully expanded card")
	}
	oldCache := block.packedRender
	palette := uitheme.Default()
	palette.AssistantCardBg = "17"
	ApplyTheme(palette)
	fresh := &Block{ID: block.ID, Type: block.Type, Content: block.Content}
	if got, expected := block.RenderRange(100, "", 120, 160), fresh.Render(100, "")[120:160]; !slices.Equal(got, expected) {
		t.Fatal("theme change reused stale rows")
	}
	if block.packedRender == oldCache {
		t.Fatal("theme change retained old cache")
	}
	store, err := newViewportSpillStore()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	viewport.spill = store
	if !viewport.spillBlock(block) || block.packedRender != nil {
		t.Fatal("spill retained packed rows")
	}
	if got, expected := block.RenderRange(100, "", 120, 160), fresh.Render(100, "")[120:160]; !slices.Equal(got, expected) {
		t.Fatal("spill restore changed rows")
	}
}

func TestPackedRenderHotBytesMemoSurvivesRepeatedRanges(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{ID: 5, Type: BlockAssistant, Content: strings.Repeat("A paragraph with **emphasis** and ordinary words.\n\n", 500) + strings.Repeat("Short.\n\n", 100)}
	if block.LineCount(100) == 0 || block.packedRender == nil {
		t.Fatal("expected packed rows")
	}
	_ = block.RenderRange(100, "", 0, 40) // decodes the first chunk
	before := block.estimatedHotBytes()
	if !block.hotBytesMemoValid {
		t.Fatal("hot bytes memo missing after decode")
	}
	for range 3 {
		_ = block.RenderRange(100, "", 0, 40) // hot window hit
	}
	if !block.hotBytesMemoValid {
		t.Fatal("memo invalidated without a new decode")
	}
	if got := block.estimatedHotBytes(); got != before {
		t.Fatalf("hot bytes changed without a new decode: %d, want %d", got, before)
	}
	_ = block.RenderRange(100, "", 512, 552) // decodes another chunk
	if got := block.estimatedHotBytes(); got == before {
		t.Fatalf("decoded window did not change hot bytes: %d", got)
	}
	for _, start := range []int{256, 768, block.LineCount(100) - 40, 0} {
		_ = block.RenderRange(100, "", start, start+40)
		got := block.estimatedHotBytes()
		block.hotBytesMemoValid = false
		if want := block.estimatedHotBytes(); got != want {
			t.Fatalf("hot bytes after window %d = %d, want full estimate %d", start, got, want)
		}
		if block.packedRender.hotDirty {
			t.Fatal("estimate did not consume the decoded window change")
		}
	}
}
