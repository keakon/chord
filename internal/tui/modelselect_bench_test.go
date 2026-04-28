package tui

import (
	"fmt"
	"testing"
)

func benchmarkModelForModelSelectDialog() Model {
	m := benchmarkModelForView()
	m.mode = ModeModelSelect
	options := make([]ModelSelectOption, 0, 18)
	for p := 0; p < 3; p++ {
		provider := fmt.Sprintf("Provider-%d", p+1)
		options = append(options, ModelSelectOption{Label: provider, Provider: provider, Header: true})
		for i := 0; i < 5; i++ {
			options = append(options, ModelSelectOption{
				Label:     fmt.Sprintf("model-%d-%d", p+1, i+1),
				Value:     fmt.Sprintf("provider-%d/model-%d", p+1, i+1),
				Provider:  provider,
				ModelID:   fmt.Sprintf("model-%d-%d", p+1, i+1),
				Context:   128000,
				Output:    16000,
				IsCurrent: p == 0 && i == 0,
			})
		}
	}
	m.modelSelect.allOptions = options
	m.modelSelect.options = options
	m.modelSelect.searchInput = "model"
	m.modelSelect.table = newModelSelectTable(options, m.modelSelectMaxVisible())
	if m.modelSelect.table != nil {
		m.modelSelect.table.list.SetCursor(modelSelectCursorIndex(options, "provider-1/model-1"))
	}
	return m
}

func BenchmarkRenderModelSelectDialogOpen(b *testing.B) {
	m := benchmarkModelForModelSelectDialog()
	_ = m.renderModelSelectDialog()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.renderModelSelectDialog()
	}
}
