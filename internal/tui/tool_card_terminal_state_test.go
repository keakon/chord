package tui

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
)

func TestToolResultSummaryLineShowsTerminalStates(t *testing.T) {
	tests := []struct {
		name string
		blk  *Block
		want string
	}{
		{
			name: "cancelled generic",
			blk:  &Block{ToolName: "write", ResultDone: true, ResultStatus: agent.ToolResultStatusCancelled, ResultContent: "cancelled"},
			want: "Cancelled",
		},
		{
			name: "spawn failed",
			blk:  &Block{ToolName: "spawn", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "spawn_stop failed",
			blk:  &Block{ToolName: "spawn_stop", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "delegate failed",
			blk:  &Block{ToolName: "delegate", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "grep failed",
			blk:  &Block{ToolName: "grep", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "glob failed",
			blk:  &Block{ToolName: "glob", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "lsp failed",
			blk:  &Block{ToolName: "lsp", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "cancel failed",
			blk:  &Block{ToolName: "cancel", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "notify failed",
			blk:  &Block{ToolName: "notify", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "spawn started",
			blk:  &Block{ToolName: "spawn", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "job started"},
			want: "Started",
		},
		{
			name: "delegate done summary",
			blk:  &Block{ToolName: "delegate", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, DoneSummary: "done"},
			want: "Done",
		},
		{
			name: "grep count",
			blk:  &Block{ToolName: "grep", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "a.go:1:one\nb.go:2:two"},
			want: "2 matches",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatToolResultSummaryLine(tt.blk); got != tt.want {
				t.Fatalf("formatToolResultSummaryLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpandedToolResultRendersTerminalStateSummary(t *testing.T) {
	block := &Block{
		Type:          BlockToolCall,
		ToolName:      "spawn",
		Collapsed:     false,
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "spawn failed",
	}
	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"spawn", "✗", "Error:", "spawn failed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded tool card missing %q; got:\n%s", want, joined)
		}
	}
}
