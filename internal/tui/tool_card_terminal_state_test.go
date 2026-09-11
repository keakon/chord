package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
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
			name: "job_kill failed",
			blk:  &Block{ToolName: tools.NameJobKill, ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "shell failed",
			blk:  &Block{ToolName: tools.NameShell, ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
			want: "",
		},
		{
			name: "job_output failed",
			blk:  &Block{ToolName: tools.NameJobOutput, ResultDone: true, ResultStatus: agent.ToolResultStatusError, ResultContent: "boom"},
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
			// The shell card owns its own exit/output surface, and the
			// collapsed card names a background handle instead.
			name: "shell success has no inline summary",
			blk:  &Block{ToolName: tools.NameShell, ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "ok"},
			want: "",
		},
		{
			name: "job_output success has no inline summary",
			blk:  &Block{ToolName: tools.NameJobOutput, ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "out\n[status: completed]"},
			want: "",
		},
		{
			name: "job_kill success names the requested stop",
			blk:  &Block{ToolName: tools.NameJobKill, ResultDone: true, ResultStatus: agent.ToolResultStatusSuccess, ResultContent: "job job-64 stopping (cancelled by job_kill)\n[status: stopping]"},
			want: "Stop requested",
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

func TestParseJobResultID(t *testing.T) {
	tests := []struct {
		name   string
		result string
		want   string
	}{
		{
			name:   "background handle",
			result: "[background job job-64] promoted after the foreground budget",
			want:   "job-64",
		},
		{
			name:   "leading blank lines are skipped",
			result: "\n  [background job job-2] promoted  \n",
			want:   "job-2",
		},
		{
			name:   "empty",
			result: "",
			want:   "",
		},
		{
			name:   "plain output is not a handle",
			result: "hello\nworld",
			want:   "",
		},
		{
			name:   "handle is not the first line",
			result: "output line\n[background job job-9] promoted",
			want:   "",
		},
		{
			name:   "empty id",
			result: "[background job ] promoted",
			want:   "",
		},
		{
			name:   "multi-word id",
			result: "[background job job-64 extra] promoted",
			want:   "",
		},
		{
			name:   "missing closing bracket",
			result: "[background job job-64",
			want:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseJobResultID(tt.result); got != tt.want {
				t.Fatalf("parseJobResultID(%q) = %q, want %q", tt.result, got, tt.want)
			}
		})
	}
}

// TestParseJobResultIDTruncatesOversizedID asserts the truncation contract
// rather than a fixed cut point. The cap is a display-width budget and the
// ellipsis is an East Asian ambiguous-width rune: go-runewidth gives it two
// columns under a CJK locale (LANG=zh_CN.UTF-8, RUNEWIDTH_EASTASIAN=1) and one
// otherwise, so the exact number of retained characters is environment
// dependent while the budget it must respect is not.
func TestParseJobResultIDTruncatesOversizedID(t *testing.T) {
	id := strings.Repeat("x", 40)
	got := parseJobResultID("[background job " + id + "] promoted")
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("parseJobResultID(oversized) = %q, want a truncated value ending in an ellipsis", got)
	}
	if width := runewidth.StringWidth(got); width > jobResultIDMaxWidth {
		t.Fatalf("truncated ID %q has display width %d, want at most %d", got, width, jobResultIDMaxWidth)
	}
	if kept := strings.TrimSuffix(got, "…"); kept == "" || !strings.HasPrefix(id, kept) {
		t.Fatalf("truncated ID %q is not a non-empty prefix of %q", got, id)
	}
}

// TestCollapsedShellCardNamesItsJobID covers the reason the collapsed shell
// body carries the ID at all: the job handle must be readable without expanding,
// because it is the argument every later job_output / job_kill call needs.
func TestCollapsedShellCardNamesItsJobID(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameShell,
		Collapsed:              true,
		Content:                `{"command":".venv/bin/python -u step1_local_run.py","description":"batch run"}`,
		ResultContent:          "[background job job-64] promoted after the foreground budget",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: false,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(plain, "Background job job-64") {
		t.Fatalf("collapsed shell card should name its job id; got:\n%s", plain)
	}
	// The rest of the result body stays behind the disclosure toggle.
	if strings.Contains(plain, "promoted after the foreground budget") {
		t.Fatalf("collapsed shell card should not expand the result body; got:\n%s", plain)
	}
}

// TestCollapsedShellCardOmitsHandleForForegroundRun guards the other side: a
// foreground result carries no handle, so no "Background job" row may appear.
func TestCollapsedShellCardOmitsHandleForForegroundRun(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameShell,
		Collapsed:     true,
		Content:       `{"command":"echo hi","description":"quick"}`,
		ResultContent: "hi",
		ResultDone:    true,
		ResultStatus:  agent.ToolResultStatusSuccess,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Contains(plain, "Background job") {
		t.Fatalf("foreground shell card must not claim a background job; got:\n%s", plain)
	}
}

// TestCollapsedJobKillCardKeepsOneID guards the asymmetry with shell: the job ID
// is an argument here, so the header already names it and the summary must stay
// a bare "Stop requested" label instead of echoing it a second time.
func TestCollapsedJobKillCardKeepsOneID(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameJobKill,
		Collapsed:              true,
		Content:                `{"job_id":"job-64"}`,
		ResultContent:          "job job-64 stopping (cancelled by job_kill)\n[status: stopping]",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: false,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.Count(plain, "job-64") != 1 {
		t.Fatalf("job_kill should name the job ID exactly once; got %d:\n%s", strings.Count(plain, "job-64"), plain)
	}
	if !strings.Contains(plain, "Stop requested") {
		t.Fatalf("job_kill lost its state label; got:\n%s", plain)
	}
}

func TestExpandedShellCardShowsFullResultBody(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameShell,
		Collapsed:              false,
		Content:                `{"command":".venv/bin/python -u step1_local_run.py","description":"batch run"}`,
		ResultContent:          "[background job job-64] promoted after the foreground budget",
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusSuccess,
		ToolCallDetailExpanded: true,
	}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{"Command", "step1_local_run.py", "Output", "[background job job-64]", "Exit: 0"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expanded shell card missing %q; got:\n%s", want, plain)
		}
	}
}

func TestExpandedToolResultRendersTerminalStateSummary(t *testing.T) {
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               tools.NameJobKill,
		Collapsed:              false,
		Content:                `{"job_id":"job-64"}`,
		ResultDone:             true,
		ResultStatus:           agent.ToolResultStatusError,
		ResultContent:          "job not found",
		ToolCallDetailExpanded: true,
	}
	joined := stripANSI(strings.Join(block.Render(100, ""), "\n"))
	for _, want := range []string{tools.NameJobKill, "✗", "Error:", "job not found"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expanded tool card missing %q; got:\n%s", want, joined)
		}
	}
}
