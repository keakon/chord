package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func benchmarkModelForSessionSelectDialog() Model {
	m := benchmarkModelForView()
	m.mode = ModeSessionSelect
	options := make([]agent.SessionSummary, 0, 18)
	baseTime := time.Date(2026, 4, 7, 12, 0, 0, 0, time.Local)
	for i := 0; i < 18; i++ {
		options = append(options, agent.SessionSummary{
			ID:               fmt.Sprintf("sess-%02d", i+1),
			FirstUserMessage: fmt.Sprintf("session preview line %02d", i+1),
			LastModTime:      baseTime.Add(-time.Duration(i) * time.Hour),
		})
	}
	m.sessionSelect = sessionSelectState{
		options:      options,
		searchCorpus: buildSessionSearchCorpus(options),
		list:         NewOverlayList(nil, m.sessionSelectMaxVisible()),
		prevMode:     ModeInsert,
	}
	m.rebuildSessionSelectFilteredView(false)
	if m.sessionSelect.list != nil {
		m.sessionSelect.list.SetCursor(0)
	}
	return m
}

func BenchmarkRenderSessionSelectDialogOpen(b *testing.B) {
	m := benchmarkModelForSessionSelectDialog()
	_ = m.renderSessionSelectDialog()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderSessionSelectDialog()
	}
}
