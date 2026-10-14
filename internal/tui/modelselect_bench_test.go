package tui

import (
	"fmt"
	"testing"
)

func benchmarkModelForPoolSelectDialog(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
	tb.Cleanup(func() { _ = m.Close() })
	m.mode = ModeModelSelect
	poolNames := make([]string, 0, 10)
	for i := range 10 {
		poolNames = append(poolNames, fmt.Sprintf("pool-%d", i+1))
	}
	m.modelSelect = modelSelectState{
		poolNames:  poolNames,
		poolCursor: 0,
		prevMode:   ModeNormal,
	}
	return m
}

func BenchmarkRenderPoolSelectDialogOpen(b *testing.B) {
	m := benchmarkModelForPoolSelectDialog(b)
	_ = m.renderModelSelectDialog()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderModelSelectDialog()
	}
}
