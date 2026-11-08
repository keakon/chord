package tui

import (
	"testing"

	"github.com/keakon/chord/internal/permission"
)

func benchmarkModelForConfirmDialog(tb testing.TB) Model {
	tb.Helper()
	m := benchmarkModelForView(tb)
	tb.Cleanup(func() { _ = m.Close() })
	m.mode = ModeConfirm
	m.confirm = confirmState{
		request: &ConfirmRequest{
			ToolName:            "Write",
			ArgsJSON:            `{"path":"docs/guides/performance.md","content":"updated"}`,
			NeedsApproval:       []string{"docs/guides/performance.md"},
			AlreadyAllowed:      []string{"README.md"},
			NeedsApprovalRules:  []permission.Rule{{Permission: "write", Pattern: "docs/*", Action: permission.ActionAsk}},
			AlreadyAllowedRules: []permission.Rule{{Permission: "*", Pattern: "*", Action: permission.ActionAllow}},
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
