package tui

import (
	"strings"
	"testing"

	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestAssistantVirtualRejectionCachedAndInvalidated(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := virtualAssistantTestContent(220) + "**bold**\n"
	block := &Block{Type: BlockAssistant, Content: content}
	v := NewViewport(100, 24)
	v.AppendBlock(block)
	want := v.Render("", nil, -1, 0, "")
	rejected := block.virtualAssistant
	if rejected == nil || rejected.valid {
		t.Fatal("expected cached rejection for large complex Markdown")
	}
	for range 3 {
		if got := v.Render("", nil, -1, 0, ""); got != want {
			t.Fatal("warm viewport changed output")
		}
		if block.virtualAssistant != rejected {
			t.Fatal("warm viewport repeated eligibility work")
		}
	}
	for _, change := range []func(){
		func() { block.Content += "more text" },
		func() { block.ID++ },
		func() { ApplyTheme(uitheme.Default()) },
	} {
		change()
		if _, ok := block.assistantVirtualLayout(100); ok || block.virtualAssistant == rejected {
			t.Fatal("rejection cache did not invalidate")
		}
		rejected = block.virtualAssistant
	}
	if _, ok := block.assistantVirtualLayout(80); ok || block.virtualAssistant == rejected {
		t.Fatal("width change did not invalidate rejection")
	}
	block.Content = virtualAssistantTestContent(220)
	if _, ok := block.assistantVirtualLayout(80); !ok {
		t.Fatal("eligible content remained rejected")
	}
	block.ThinkingParts = []string{"Reasoning summary"}
	block.InvalidateCache()
	if _, ok := block.assistantVirtualLayout(80); ok {
		t.Fatal("thinking content must disable virtual rendering")
	}
	block.ThinkingParts = nil
	block.InvalidateCache()
	if _, ok := block.assistantVirtualLayout(80); !ok {
		t.Fatal("cache invalidation retained a rejected candidate")
	}
}

func BenchmarkViewportRejectedAssistantWarmRender(b *testing.B) {
	ApplyTheme(uitheme.Default())
	v := NewViewport(100, 24)
	v.AppendBlock(&Block{Type: BlockAssistant, Content: strings.Repeat("Ordinary paragraph with enough words to exercise the viewport render path.\n\n", 230) + "**bold**\n"})
	v.Render("", nil, -1, 0, "")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v.Render("", nil, -1, 0, "")
	}
}
