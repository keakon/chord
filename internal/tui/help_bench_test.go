package tui

import "testing"

func benchmarkModelForHelpView(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
	tb.Cleanup(func() { _ = m.Close() })
	m.mode = ModeHelp
	m.help = helpState{prevMode: ModeInsert}
	return m
}

func BenchmarkRenderHelpViewOpen(b *testing.B) {
	m := benchmarkModelForHelpView(b)
	_ = m.renderHelpView()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderHelpView()
	}
}
