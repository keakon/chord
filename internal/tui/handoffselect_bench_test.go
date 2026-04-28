package tui

import "testing"

func benchmarkModelForHandoffDialog() Model {
	m := benchmarkModelForView()
	m.mode = ModeHandoffSelect
	options := []handoffOption{
		{Name: "builder", IsDefault: true},
		{Name: "reviewer"},
		{Name: "planner"},
		{Name: "tester"},
	}
	m.handoffSelect = handoffSelectState{
		options:  options,
		list:     NewOverlayList(handoffItems(options), m.handoffSelectMaxVisible()),
		planPath: "docs/plans/example.md",
		prevMode: ModeInsert,
	}
	if m.handoffSelect.list != nil {
		m.handoffSelect.list.SetCursor(0)
	}
	return m
}

func BenchmarkRenderHandoffSelectDialogOpen(b *testing.B) {
	m := benchmarkModelForHandoffDialog()
	_ = m.renderHandoffSelectDialog()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderHandoffSelectDialog()
	}
}
