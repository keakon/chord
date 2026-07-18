package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	tea "github.com/keakon/bubbletea/v2"
	uv "github.com/keakon/ultraviolet"
)

func TestBlockLabelsShowOneBasedSequenceForAllCardTypes(t *testing.T) {
	ApplyTheme(DefaultTheme())

	blocks := []*Block{
		{ID: 100, DisplaySequence: 1, Type: BlockUser, Content: "hello"},
		{ID: 101, DisplaySequence: 2, Type: BlockAssistant, Content: "hi"},
		{ID: 102, DisplaySequence: 3, Type: BlockThinking, Content: "reasoning"},
		{ID: 103, DisplaySequence: 4, Type: BlockToolCall, ToolName: "shell", Content: `{"command":"echo hi"}`},
		{ID: 104, DisplaySequence: 5, Type: BlockToolResult, Content: "ok"},
		{ID: 105, DisplaySequence: 6, Type: BlockError, Content: "boom"},
		{ID: 106, DisplaySequence: 7, Type: BlockStatus, StatusTitle: "STATUS", Content: "body"},
		{ID: 107, DisplaySequence: 8, Type: BlockCompactionSummary, Content: "summary"},
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
	cardEnd := len(cells) - ErrorCardStyle.GetMarginRight()
	for i := msgEndCol + 1; i < cardEnd; i++ {
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

func TestErrorCardUsesConfiguredNormalModeBinding(t *testing.T) {
	m := NewModelWithSize(nil, 100, 30)
	block := &Block{ID: 1, Type: BlockError, Content: "boom"}
	m.viewport.AppendBlock(block)

	initial := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(initial, "normal mode · ctrl+e: error details") {
		t.Fatalf("default error card hint missing: %q", initial)
	}

	km := KeyMapFromConfig(map[string][]string{"error_panel": {"alt+e"}})
	m.SetKeyMap(km)

	configured := stripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(configured, "normal mode · alt+e: error details") || strings.Contains(configured, "ctrl+e: error details") {
		t.Fatalf("error card hint did not follow configured binding: %q", configured)
	}

	m.mode = ModeInsert
	_ = m.handleInsertKey(tea.KeyPressMsg(tea.Key{Code: 'e', Mod: tea.ModAlt}))
	if m.mode != ModeInsert {
		t.Fatalf("normal-only error binding changed Insert mode to %v", m.mode)
	}

	m.mode = ModeNormal
	_ = m.handleNormalKey(tea.KeyPressMsg(tea.Key{Code: 'e', Mod: tea.ModAlt}))
	if m.mode != ModeErrorPanel {
		t.Fatalf("configured normal binding left mode at %v, want %v", m.mode, ModeErrorPanel)
	}
	if m.errorPanel.prevMode != ModeNormal {
		t.Fatalf("error panel previous mode = %v, want %v", m.errorPanel.prevMode, ModeNormal)
	}
}
