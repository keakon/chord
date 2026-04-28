package tui

import "testing"

func BenchmarkModelViewAtMentionPopupOpen(b *testing.B) {
	m := benchmarkModelForView()
	m.mode = ModeInsert
	m.atMentionOpen = true
	m.atMentionQuery = "doc"
	m.atMentionList = NewOverlayList([]OverlayListItem{
		{Label: "docs/ARCHITECTURE.md"},
		{Label: "docs/guides/index.md"},
		{Label: "docs/pitfalls/tui-performance.md"},
		{Label: "internal/tui/app_render.go"},
	}, 10)
	_ = m.View()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}
