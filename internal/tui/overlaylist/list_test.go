package overlaylist

import "testing"

func benchmarkOverlayList() *List {
	items := make([]Item, 24)
	for i := range items {
		items[i] = Item{ID: string(rune('a' + i%26)), Label: "item label"}
	}
	return New(items, 10)
}

func TestOverlayListRenderCacheInvalidatesOnCursorChange(t *testing.T) {
	l := New([]Item{{Label: "alpha"}, {Label: "beta"}}, 10)
	first := l.Render(24, testStyles)
	l.CursorDown()
	second := l.Render(24, testStyles)
	if first == second {
		t.Fatal("Render() did not change after cursor moved")
	}
}

func TestOverlayListRenderCacheInvalidatesOnSetItems(t *testing.T) {
	l := New([]Item{{Label: "alpha"}}, 10)
	first := l.Render(24, testStyles)
	l.SetItems([]Item{{Label: "beta"}})
	second := l.Render(24, testStyles)
	if first == second {
		t.Fatal("Render() did not change after items changed")
	}
}

func BenchmarkOverlayListRenderCacheHit(b *testing.B) {
	l := benchmarkOverlayList()
	_ = l.Render(32, testStyles)
	b.ReportAllocs()
	for b.Loop() {
		_ = l.Render(32, testStyles)
	}
}

func BenchmarkOverlayListRenderCacheMiss(b *testing.B) {
	l := benchmarkOverlayList()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		l.SetCursor(i % l.Len())
		_ = l.Render(32, testStyles)
	}
}
