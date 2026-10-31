package tui

import (
	"reflect"
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestAssistantBodyVisibilityCacheReleased(t *testing.T) {
	ApplyTheme(uitheme.Default())
	for _, spill := range []bool{true, false} {
		name := "release"
		if spill {
			name = "spill"
		}
		t.Run(name, func(t *testing.T) {
			store, err := newViewportSpillStore()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			content := strings.Repeat("sample body text\n", 256)
			block := &Block{ID: 1, Type: BlockAssistant, Content: content}
			rendered := append([]string(nil), block.Render(80, "")...)
			if !block.bodyVisibilityValid || block.bodyVisibilityKey != content {
				t.Fatal("render did not populate body visibility cache")
			}
			if spill {
				v := &Viewport{width: 80, spill: store, hotBytes: block.estimatedHotBytes()}
				if !v.spillBlock(block) {
					t.Fatal("spill failed")
				}
				if block.Content != "" || block.estimatedHotBytes() != 0 {
					t.Fatal("cold block retained content or hot-byte accounting")
				}
			} else {
				block.ReleaseDerivedRenderCaches()
				if block.Content != content {
					t.Fatal("release changed content")
				}
			}
			if block.bodyVisibilityKey != "" || block.bodyVisibilityValid {
				t.Fatalf("visibility cache survived %s: key bytes=%d valid=%v", name, len(block.bodyVisibilityKey), block.bodyVisibilityValid)
			}
			if err := block.ensureMaterialized(); err != nil {
				t.Fatal(err)
			}
			if got := block.Render(80, ""); !reflect.DeepEqual(got, rendered) {
				t.Fatal("render changed after cache release")
			}
			if !block.bodyVisibilityValid || block.bodyVisibilityKey != content {
				t.Fatal("render did not rebuild visibility cache")
			}
		})
	}
}

func TestAssistantBodyVisibilityCacheSurvivesStreamingInvalidation(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{Type: BlockAssistant, Content: "sample partial text", Streaming: true}
	block.Render(80, "")
	if !block.bodyVisibilityValid {
		t.Fatal("render did not populate body visibility cache")
	}
	block.InvalidateCache()
	block.ReleaseDerivedRenderCaches()
	if !block.bodyVisibilityValid || block.bodyVisibilityKey != block.Content {
		t.Fatal("streaming invalidation discarded unchanged body visibility cache")
	}
}
