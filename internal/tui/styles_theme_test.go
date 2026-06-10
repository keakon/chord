package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

func TestTranscriptCardStylesShareBaseSpacing(t *testing.T) {
	ApplyTheme(DefaultTheme())

	base := transcriptCardStyle()
	baseTop, baseRight, baseBottom, baseLeft := base.GetPadding()
	if baseTop != 1 || baseRight != 1 || baseBottom != 1 || baseLeft != 1 {
		t.Fatalf("base transcript card padding = (%d,%d,%d,%d), want (1,1,1,1)", baseTop, baseRight, baseBottom, baseLeft)
	}
	if got := base.GetMarginLeft(); got != 1 {
		t.Fatalf("base transcript card left margin = %d, want 1", got)
	}
	if got := base.GetMarginBottom(); got != 1 {
		t.Fatalf("base transcript card bottom margin = %d, want 1", got)
	}

	tests := []struct {
		name         string
		style        lipgloss.Style
		wantPadding  [4]int
		wantLeftEdge int
	}{
		{name: "user", style: UserCardStyle, wantPadding: [4]int{1, 1, 1, 1}, wantLeftEdge: 2},
		{name: "assistant", style: AssistantCardStyle, wantPadding: [4]int{1, 1, 1, 1}, wantLeftEdge: 2},
		{name: "compaction summary", style: CompactionSummaryCardStyle, wantPadding: [4]int{1, 1, 1, 1}, wantLeftEdge: 2},
		{name: "error", style: ErrorCardStyle, wantPadding: [4]int{1, 1, 1, 1}, wantLeftEdge: 2},
		{name: "tool", style: ToolBlockStyle, wantPadding: [4]int{1, 2, 1, 1}, wantLeftEdge: 2},
		{name: "thinking", style: ThinkingCardStyle, wantPadding: [4]int{1, 1, 1, 2}, wantLeftEdge: 3},
	}
	for _, tt := range tests {
		top, right, bottom, left := tt.style.GetPadding()
		gotPadding := [4]int{top, right, bottom, left}
		if gotPadding != tt.wantPadding {
			t.Fatalf("%s padding = %v, want %v", tt.name, gotPadding, tt.wantPadding)
		}
		if got := tt.style.GetMarginLeft(); got != base.GetMarginLeft() {
			t.Fatalf("%s left margin = %d, want shared base margin %d", tt.name, got, base.GetMarginLeft())
		}
		if got := tt.style.GetMarginBottom(); got != base.GetMarginBottom() {
			t.Fatalf("%s bottom margin = %d, want shared base margin %d", tt.name, got, base.GetMarginBottom())
		}
		if got := tt.style.GetMarginLeft() + tt.style.GetPaddingLeft(); got != tt.wantLeftEdge {
			t.Fatalf("%s content left edge = %d, want %d", tt.name, got, tt.wantLeftEdge)
		}
	}

	toolTop, toolRight, toolBottom, toolLeft := ToolBlockStyle.GetPadding()
	if toolTop != baseTop || toolBottom != baseBottom || toolLeft != baseLeft {
		t.Fatalf("tool card must keep transcript base vertical/left padding; got (%d,%d,%d,%d), base (%d,%d,%d,%d)", toolTop, toolRight, toolBottom, toolLeft, baseTop, baseRight, baseBottom, baseLeft)
	}
	if toolRight != 2 {
		t.Fatalf("tool card right padding = %d, want 2", toolRight)
	}
}
