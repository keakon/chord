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
			blk:  &Block{ToolName: "Write", ResultDone: true, ResultStatus: agent.ToolResultStatusCancelled, ResultContent: "cancelled"},
			want: "Cancelled",
		},
		{
			name: "spawn failed",
			blk:  &Block{ToolName: "Spawn", ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "Failed",
		},
		{
			name: "spawn started",
			blk:  &Block{ToolName: "Spawn", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "job started"},
			want: "Started",
		},
		{
			name: "delegate done summary",
			blk:  &Block{ToolName: "Delegate", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, DoneSummary: "done"},
			want: "Done",
		},
		{
			name: "grep count",
			blk:  &Block{ToolName: "Grep", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "a.go:1:one\nb.go:2:two"},
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
		ToolName:      "Spawn",
		Collapsed:     false,
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusError,
		ResultContent: "spawn failed",
	}
	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"Spawn", "Failed", "Error:", "spawn failed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded tool card missing %q; got:\n%s", want, joined)
		}
	}
}
