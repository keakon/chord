package tui

import (
	"testing"

	"github.com/keakon/chord/internal/tui/overlaylist"
)

func BenchmarkModelViewAtMentionPopupOpen(b *testing.B) {
	m := benchmarkModelForView(b)
	b.Cleanup(func() { _ = m.Close() })
	m.mode = ModeInsert
	m.atMentionOpen = true
	m.atMentionQuery = "doc"
	m.atMentionList = overlaylist.New([]overlaylist.Item{
		{Label: "docs/ARCHITECTURE.md"},
		{Label: "docs/guides/index.md"},
		{Label: "docs/pitfalls/tui-performance.md"},
		{Label: "internal/tui/app_render.go"},
	}, 10)
	_ = m.View()

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = m.View()
	}
}
