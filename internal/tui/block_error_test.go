package tui

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestBlockLabelsShowOneBasedSequenceForAllCardTypes(t *testing.T) {
	ApplyTheme(DefaultTheme())

	blocks := []*Block{
		{ID: 0, Type: BlockUser, Content: "hello"},
		{ID: 1, Type: BlockAssistant, Content: "hi"},
		{ID: 2, Type: BlockThinking, Content: "reasoning"},
		{ID: 3, Type: BlockToolCall, ToolName: "Shell", Content: `{"command":"echo hi"}`},
		{ID: 4, Type: BlockToolResult, Content: "ok"},
		{ID: 5, Type: BlockError, Content: "boom"},
		{ID: 6, Type: BlockStatus, StatusTitle: "STATUS", Content: "body"},
		{ID: 7, Type: BlockCompactionSummary, Content: "summary"},
	}
	want := []string{"USER #1", "ASSISTANT #2", "THINKING #3", "TOOL CALL #4", "TOOL RESULT #5", "ERROR #6", "STATUS #7", "CONTEXT SUMMARY #8"}

	for i, block := range blocks {
		got := stripANSI(strings.Join(block.Render(100, ""), "\n"))
		if !strings.Contains(got, want[i]) {
			t.Fatalf("block type %v render missing label %q:\n%s", block.Type, want[i], got)
		}
	}
}

func TestRenderErrorCardMessageLineKeepsBackgroundOnTrailingPadding(t *testing.T) {
	ApplyTheme(DefaultTheme())

	msg := "LLM stream failed: all API keys cooling down, retry after 28.363159s"
	block := &Block{Type: BlockError, Content: msg}
	lines := block.Render(149, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered error block")
	}

	var target string
	for _, line := range lines {
		if strings.Contains(stripANSI(line), msg) {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("failed to locate error message line in render output: %q", strings.Join(lines, "\n"))
	}

	plain := stripANSI(target)
	start := strings.Index(plain, msg)
	if start < 0 {
		t.Fatalf("message %q not found in plain line %q", msg, plain)
	}
	msgEndCol := ansi.StringWidth(plain[:start+len(msg)]) - 1
	if msgEndCol < 0 {
		t.Fatalf("invalid message end col: %d", msgEndCol)
	}

	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	errorBg := colorOfTheme(currentTheme.ErrorCardBg)
	trailingSpaces := 0
	for i := msgEndCol + 1; i < len(cells); i++ {
		cell := cells[i]
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		trailingSpaces++
		if !colorsEqual(cell.Style.Bg, errorBg) {
			t.Fatalf("trailing space at col %d background = %v, want error bg %v", i, cell.Style.Bg, errorBg)
		}
	}
	if trailingSpaces == 0 {
		t.Fatal("expected trailing padding spaces after error message")
	}
}
