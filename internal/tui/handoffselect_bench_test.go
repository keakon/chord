package tui

import (
	"testing"

	"github.com/keakon/chord/internal/tui/overlaylist"
)

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
		planPath: "docs/plans/example.md",
		prevMode: ModeInsert,
	}
	m.handoffSelect.selector.list = overlaylist.New(handoffItems(options), m.handoffSelectMaxVisible())
	if m.handoffSelect.selector.list != nil {
		m.handoffSelect.selector.list.SetCursor(0)
	}
	return m
}

func BenchmarkRenderHandoffSelectDialogOpen(b *testing.B) {
	m := benchmarkModelForHandoffDialog()
	_ = m.renderHandoffSelectDialog()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderHandoffSelectDialog()
	}
}
