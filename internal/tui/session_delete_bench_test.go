package tui

import (
	"testing"
	"time"

	"github.com/keakon/chord/internal/agent"
)

func benchmarkModelForSessionDeleteConfirmDialog() Model {
	m := benchmarkModelForView()
	m.mode = ModeSessionDeleteConfirm
	baseTime := time.Date(2026, 4, 7, 12, 0, 0, 0, time.Local)
	session := agent.SessionSummary{
		ID:               "sess-42",
		FirstUserMessage: "This is a longer preview line used to exercise wrapping inside the delete confirmation dialog.",
		LastModTime:      baseTime,
		ForkedFrom:       "sess-17",
	}
	m.sessionDeleteConfirm = sessionDeleteConfirmState{
		session:  &session,
		prevMode: ModeSessionSelect,
	}
	return m
}

func BenchmarkRenderSessionDeleteConfirmDialogOpen(b *testing.B) {
	m := benchmarkModelForSessionDeleteConfirmDialog()
	_ = m.renderSessionDeleteConfirmDialog()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderSessionDeleteConfirmDialog()
	}
}
