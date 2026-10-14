package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tui/overlaylist"
)

func benchmarkModelForSessionSelectDialog(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
	tb.Cleanup(func() { _ = m.Close() })
	m.mode = ModeSessionSelect
	options := make([]agent.SessionSummary, 0, 18)
	baseTime := time.Date(2026, 4, 7, 12, 0, 0, 0, time.Local)
	for i := range 18 {
		options = append(options, agent.SessionSummary{
			ID:               fmt.Sprintf("sess-%02d", i+1),
			FirstUserMessage: fmt.Sprintf("session preview line %02d", i+1),
			LastModTime:      baseTime.Add(-time.Duration(i) * time.Hour),
		})
	}
	m.sessionSelect = sessionSelectState{
		options:      options,
		searchCorpus: buildSessionSearchCorpus(options),
		prevMode:     ModeInsert,
	}
	m.sessionSelect.selector.list = overlaylist.New(nil, m.sessionSelectMaxVisible())
	m.rebuildSessionSelectFilteredView(false)
	if m.sessionSelect.selector.list != nil {
		m.sessionSelect.selector.list.SetCursor(0)
	}
	return m
}

func BenchmarkRenderSessionSelectDialogOpen(b *testing.B) {
	m := benchmarkModelForSessionSelectDialog(b)
	_ = m.renderSessionSelectDialog()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderSessionSelectDialog()
	}
}
