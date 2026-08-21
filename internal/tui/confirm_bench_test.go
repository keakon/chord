package tui

import "testing"

func benchmarkModelForConfirmDialog() Model {
	m := benchmarkModelForView()
	m.mode = ModeConfirm
	m.confirm = confirmState{
		request: &ConfirmRequest{
			ToolName:       "Write",
			ArgsJSON:       `{"path":"docs/guides/performance.md","content":"updated"}`,
			NeedsApproval:  []string{"docs/guides/performance.md"},
			AlreadyAllowed: []string{"README.md"},
		},
		prevMode: ModeInsert,
	}
	return m
}

func BenchmarkRenderConfirmDialogOpen(b *testing.B) {
	m := benchmarkModelForConfirmDialog()
	_ = m.renderConfirmDialog()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderConfirmDialog()
	}
}
