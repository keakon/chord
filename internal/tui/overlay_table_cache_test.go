package tui

import "testing"

func benchmarkOverlayTable() *OverlayTable {
	items := make([]OverlayTableItem, 16)
	for i := range items {
		items[i] = OverlayTableItem{
			OverlayListItem: OverlayListItem{ID: string(rune('a' + i%26)), Label: "row"},
			Cells:           []string{"name", "value", "detail"},
		}
	}
	return NewOverlayTable([]TableColumn{{Title: "Name"}, {Title: "Value", Align: 1}, {Title: "Detail"}}, items, 8)
}

func TestOverlayTableRenderCacheInvalidatesOnCursorChange(t *testing.T) {
	tbl := benchmarkOverlayTable()
	first := tbl.Render(48)
	tbl.CursorDown()
	second := tbl.Render(48)
	if first == second {
		t.Fatal("Render() did not change after cursor moved")
	}
}

func TestOverlayTableRenderCacheInvalidatesOnSetItems(t *testing.T) {
	tbl := benchmarkOverlayTable()
	first := tbl.Render(48)
	tbl.SetItems([]OverlayTableItem{{OverlayListItem: OverlayListItem{Label: "beta"}, Cells: []string{"beta", "2", "changed"}}})
	second := tbl.Render(48)
	if first == second {
		t.Fatal("Render() did not change after items changed")
	}
}

func BenchmarkOverlayTableRenderCacheHit(b *testing.B) {
	tbl := benchmarkOverlayTable()
	_ = tbl.Render(48)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tbl.Render(48)
	}
}

func BenchmarkOverlayTableRenderCacheMiss(b *testing.B) {
	tbl := benchmarkOverlayTable()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		tbl.CursorDown()
		_ = tbl.Render(48)
	}
}
