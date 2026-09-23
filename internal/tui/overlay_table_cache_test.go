package tui

import "testing"

func benchmarkOverlayTable() *OverlayTable {
	items := make([]OverlayTableItem, 16)
	for i := range items {
		items[i] = OverlayTableItem{
			ID: string(rune('a' + i%26)), Label: "row",
			Cells: []string{"name", "value", "detail"},
		}
	}
	return NewOverlayTable([]TableColumn{{Title: "Name"}, {Title: "Value", Align: 1}, {Title: "Detail"}}, items, 8)
}

func TestOverlayTableRenderCacheInvalidatesOnShowSelectionChange(t *testing.T) {
	tbl := benchmarkOverlayTable()
	first := tbl.Render(48)
	tbl.SetShowSelection(false)
	second := tbl.Render(48)
	if first == second {
		t.Fatal("Render() did not change after the selection gutter was toggled")
	}
}

func BenchmarkOverlayTableRenderCacheHit(b *testing.B) {
	tbl := benchmarkOverlayTable()
	_ = tbl.Render(48)
	b.ReportAllocs()
	for b.Loop() {
		_ = tbl.Render(48)
	}
}

func BenchmarkOverlayTableRenderCacheMiss(b *testing.B) {
	tbl := benchmarkOverlayTable()
	b.ReportAllocs()
	showSelection := true
	for b.Loop() {
		showSelection = !showSelection
		tbl.SetShowSelection(showSelection)
		_ = tbl.Render(48)
	}
}
