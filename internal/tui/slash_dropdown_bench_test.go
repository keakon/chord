package tui

import "testing"

func benchmarkModelForSlashCompletionDropdown() Model {
	m := benchmarkModelForView()
	m.mode = ModeInsert
	m.slashCompleteSelected = 2
	return m
}

func BenchmarkRenderSlashCompletionDropdownOpen(b *testing.B) {
	m := benchmarkModelForSlashCompletionDropdown()
	value := "/"
	_ = m.renderSlashCompletionDropdown(value)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderSlashCompletionDropdown(value)
	}
}
