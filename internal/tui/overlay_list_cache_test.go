package tui

import "testing"

func benchmarkOverlayList() *OverlayList {
	items := make([]OverlayListItem, 24)
	for i := range items {
		items[i] = OverlayListItem{ID: string(rune('a' + i%26)), Label: "item label"}
	}
	return NewOverlayList(items, 10)
}

func TestOverlayListRenderCacheInvalidatesOnCursorChange(t *testing.T) {
	l := NewOverlayList([]OverlayListItem{{Label: "alpha"}, {Label: "beta"}}, 10)
	first := l.Render(24)
	l.CursorDown()
	second := l.Render(24)
	if first == second {
		t.Fatal("Render() did not change after cursor moved")
	}
}

func TestOverlayListRenderCacheInvalidatesOnSetItems(t *testing.T) {
	l := NewOverlayList([]OverlayListItem{{Label: "alpha"}}, 10)
	first := l.Render(24)
	l.SetItems([]OverlayListItem{{Label: "beta"}})
	second := l.Render(24)
	if first == second {
		t.Fatal("Render() did not change after items changed")
	}
}

func BenchmarkOverlayListRenderCacheHit(b *testing.B) {
	l := benchmarkOverlayList()
	_ = l.Render(32)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = l.Render(32)
	}
}

func BenchmarkOverlayListRenderCacheMiss(b *testing.B) {
	l := benchmarkOverlayList()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		l.SetCursor(i % l.Len())
		_ = l.Render(32)
	}
}
