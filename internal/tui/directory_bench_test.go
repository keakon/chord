package tui

import (
	"fmt"
	"testing"
)

func benchmarkModelForDirectoryView() Model {
	m := benchmarkModelForView()
	m.mode = ModeDirectory
	entries := make([]DirectoryEntry, 0, 32)
	for i := 0; i < 32; i++ {
		entries = append(entries, DirectoryEntry{BlockIndex: i, Summary: fmt.Sprintf("message summary line %02d", i+1)})
	}
	m.dirEntries = entries
	m.dirList = NewOverlayList(directoryItems(entries), m.directoryMaxVisible())
	if m.dirList != nil {
		m.dirList.SetCursor(0)
	}
	return m
}

func BenchmarkRenderDirectoryOpen(b *testing.B) {
	m := benchmarkModelForDirectoryView()
	_ = m.renderDirectory()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderDirectory()
	}
}
