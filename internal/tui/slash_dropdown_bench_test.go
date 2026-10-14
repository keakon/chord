package tui

import "testing"

func benchmarkModelForSlashCompletionDropdown(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
	m.mode = ModeInsert
	m.slashCompleteSelected = 2
	return m
}

func BenchmarkRenderSlashCompletionDropdownOpen(b *testing.B) {
	m := benchmarkModelForSlashCompletionDropdown(b)
	value := "/"
	_ = m.renderSlashCompletionDropdown(value)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderSlashCompletionDropdown(value)
	}
}
