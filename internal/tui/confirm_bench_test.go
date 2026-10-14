package tui

import "testing"

func benchmarkModelForConfirmDialog(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
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
	m := benchmarkModelForConfirmDialog(b)
	_ = m.renderConfirmDialog()
	b.ReportAllocs()
	for b.Loop() {
		_ = m.renderConfirmDialog()
	}
}
