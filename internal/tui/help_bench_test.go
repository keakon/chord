package tui

import "testing"

func benchmarkModelForHelpView() Model {
	m := benchmarkModelForView()
	m.mode = ModeHelp
	m.help = helpState{prevMode: ModeInsert}
	return m
}

func BenchmarkRenderHelpViewOpen(b *testing.B) {
	m := benchmarkModelForHelpView()
	_ = m.renderHelpView()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderHelpView()
	}
}
