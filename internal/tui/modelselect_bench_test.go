package tui

import (
	"fmt"
	"testing"
)

func benchmarkModelForPoolSelectDialog() Model {
	m := benchmarkModelForView()
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
	m := benchmarkModelForPoolSelectDialog()
	_ = m.renderModelSelectDialog()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderModelSelectDialog()
	}
}
