package tui

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/tui/overlaylist"
)

func benchmarkModelForDirectoryView(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
	m.mode = ModeDirectory
	entries := make([]DirectoryEntry, 0, 32)
	for i := range 32 {
		entries = append(entries, DirectoryEntry{BlockIndex: i, Summary: fmt.Sprintf("message summary line %02d", i+1)})
	}
	m.dirEntries = entries
	m.dirList = overlaylist.New(directoryItems(entries), m.directoryMaxVisible())
	if m.dirList != nil {
		m.dirList.SetCursor(0)
	}
	return m
}

func BenchmarkRenderDirectoryOpen(b *testing.B) {
	m := benchmarkModelForDirectoryView(b)
	_ = m.renderDirectory()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderDirectory()
	}
}
