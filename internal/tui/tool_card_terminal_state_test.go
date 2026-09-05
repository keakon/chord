package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
)

func TestToolResultSummaryLineShowsTerminalStates(t *testing.T) {
	tests := []struct {
		name string
		blk  *Block
		want string
	}{
		{
			// The shared ↳ Cancelled envelope owns the cancellation, so the
			// summary line must stay empty instead of printing it twice.
			name: "cancelled generic",
			blk:  &Block{ToolName: "write", ResultDone: true, ResultStatus: agent.ToolResultStatusCancelled, ResultContent: "cancelled"},
			want: "",
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
			name: "spawn started names its id",
			blk:  &Block{ToolName: "spawn", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "id: svc-64\nstatus: running\nlog_file: /tmp/svc-64.log\nmax_runtime: none"},
			want: "Started · svc-64",
		},
		{
			name: "spawn started falls back when id is malformed",
			blk:  &Block{ToolName: "spawn", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "id: svc-64 extra\nstatus: running"},
			want: "Started",
		},
		{
			name: "spawn_stop stopped keeps its bare label",
			// spawn_stop takes the ID as an argument, so the header already
			// names it; echoing it in the summary would just repeat it.
			blk:  &Block{ToolName: "spawn_stop", ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "id: svc-64\nstatus: cancelled"},
			want: "Stopped",
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

func TestParseSpawnResultID(t *testing.T) {
	tests := []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "spawn result",
			result: "id: svc-64\nstatus: running\nlog_file: /tmp/svc-64.log\nmax_runtime: none",
			want:   "svc-64",
		},
		{
			name:   "job result",
			result: "id: job-7\nstatus: running\nmax_runtime: 30s",
			want:   "job-7",
		},
		{
			name:   "leading blank lines are skipped",
			result: "\n  id: svc-2  \nstatus: running",
			want:   "svc-2",
		},
		{
			name:   "empty",
			result: "",
			want:   "",
		},
		{
			name:   "error text is not an id",
			result: "spawn failed: boom",
			want:   "",
		},
		{
			name:   "id is not the first key",
			result: "status: running\nid: svc-64",
			want:   "",
		},
		{
			name:   "empty id value",
			result: "id:\nstatus: running",
			want:   "",
		},
		{
			name:   "multi-word id value",
			result: "id: svc-64 extra\nstatus: running",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSpawnResultID(tt.result); got != tt.want {
				t.Fatalf("parseSpawnResultID(%q) = %q, want %q", tt.result, got, tt.want)
			}
		})
	}
}

// TestParseSpawnResultIDTruncatesOversizedID asserts the truncation contract
// rather than a fixed cut point. The cap is a display-width budget and the
// ellipsis is an East Asian ambiguous-width rune: go-runewidth gives it two
// columns under a CJK locale (LANG=zh_CN.UTF-8, RUNEWIDTH_EASTASIAN=1) and one
// otherwise, so the exact number of retained characters is environment
// dependent while the budget it must respect is not.
func TestParseSpawnResultIDTruncatesOversizedID(t *testing.T) {
	id := strings.Repeat("x", 40)
	got := parseSpawnResultID("id: " + id + "\nstatus: running")
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("parseSpawnResultID(oversized) = %q, want a truncated value ending in an ellipsis", got)
	}
	if width := runewidth.StringWidth(got); width > spawnResultIDMaxWidth {
		t.Fatalf("truncated ID %q has display width %d, want at most %d", got, width, spawnResultIDMaxWidth)
	}
	if kept := strings.TrimSuffix(got, "…"); kept == "" || !strings.HasPrefix(id, kept) {
		t.Fatalf("truncated ID %q is not a non-empty prefix of %q", got, id)
	}
}

// TestCollapsedSpawnCardNamesItsProcessID covers the reason the summary carries
// the ID at all: the process handle must be readable without expanding, because
// it is the argument every later spawn_status / spawn_stop call needs.
func TestCollapsedSpawnCardNamesItsProcessID(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "spawn",
		Collapsed:              true,
		Content:                `{"command":".venv/bin/python -u step1_local_run.py","description":"batch run"}`,
		ResultContent:          "id: svc-64\nstatus: running\nlog_file: /tmp/svc-64.log\nmax_runtime: none",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: false,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "svc-64") {
		t.Fatalf("collapsed spawn card should name its process ID; got:\n%s", plain)
	}
	if !strings.Contains(plain, "Started") {
		t.Fatalf("collapsed spawn card lost its state label; got:\n%s", plain)
	}
	// The rest of the result body stays behind the disclosure toggle.
	for _, hidden := range []string{"status: running", "log_file:", "max_runtime:"} {
		if strings.Contains(plain, hidden) {
			t.Fatalf("collapsed spawn card should not expand the result body (%q); got:\n%s", hidden, plain)
		}
	}
}

// TestCollapsedSpawnStopCardKeepsOneID guards the asymmetry with spawn: the ID
// is an argument here, so the header already names it and the summary must stay
// a bare label instead of echoing it a second time.
func TestCollapsedSpawnStopCardKeepsOneID(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "spawn_stop",
		Collapsed:              true,
		Content:                `{"id":"svc-64"}`,
		ResultContent:          "id: svc-64\nstatus: cancelled",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: false,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Count(plain, "svc-64") != 1 {
		t.Fatalf("spawn_stop should name the process ID exactly once; got %d:\n%s", strings.Count(plain, "svc-64"), plain)
	}
	if !strings.Contains(plain, "Stopped") {
		t.Fatalf("spawn_stop lost its state label; got:\n%s", plain)
	}
}

func TestExpandedSpawnCardShowsFullResultBody(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "spawn",
		Collapsed:              false,
		Content:                `{"command":".venv/bin/python -u step1_local_run.py","description":"batch run"}`,
		ResultContent:          "id: svc-64\nstatus: running\nlog_file: /tmp/svc-64.log\nmax_runtime: none",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: true,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"id: svc-64", "status: running", "log_file:", "max_runtime:"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expanded spawn card missing %q; got:\n%s", want, plain)
		}
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
