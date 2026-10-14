package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/syntax"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

// Orphaned variation selectors carry no presentation information. Display
// normalization drops them while preserving valid emoji/keycap sequences.
// These tests pin both the stripping rule and the layout consequence.

// End-to-end: a card built from orphan-bearing content must paint its surface
// across the full width, with no terminal-default columns at the right edge.
func TestCardBackgroundFillsWidthWithOrphans(t *testing.T) {
	const cardWidth = 100
	// Realistic content: prose with orphaned selectors and a markdown table.
	content := "验证结果：differing line ️1 与 line ️2 均已入列，\n\n" +
		"| 层级 | 条件 |\n|---|---|\n| 1 | line ️1 命中 |\n| 2 | line ️2 兜底 |\n"

	b := &Block{Type: BlockAssistant, Content: content}
	lines := b.Render(cardWidth, "")

	for i, l := range lines {
		n := strings.Count(l, "\ufe0f")
		if n != 0 {
			t.Fatalf("line %d still contains %d orphan selector(s) in rendered output: %q",
				i, n, l)
		}
		expanded := terminaltext.ExpandTabsANSI(l, terminaltext.TabWidth)
		if w := terminaltext.Width(expanded); w < cardWidth {
			t.Fatalf("line %d width = %d, want >= %d (card background would not reach the right edge):\n%q",
				i, w, cardWidth, l)
		}
	}
}

// End-to-end: the edit error card renders old_string/new_string through the
// code-highlighted preview, which bypasses terminaltext.Sanitize. Args carrying
// orphaned selectors must render with no selector left and a full-width
// background. This is the shape observed in the failing session: an old_string
// copied with " ️12,  ️45" where the file had "12, 45".
func TestEditErrorCardPreviewStripsOrphans(t *testing.T) {
	const cardWidth = 100
	args, err := json.Marshal(map[string]string{
		"path":       "src/sample.go",
		"old_string": "// messages: \"at lines  \ufe0f12,  \ufe0f45\" for many\nfunc old() {}\n",
		"new_string": "// replacement\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	b := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameEdit,
		Content:       string(args),
		RawArgs:       string(args),
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: old_string not found in file, even after punctuation/whitespace tolerance.",
	}

	for i, l := range b.Render(cardWidth, "") {
		if n := strings.Count(l, "\ufe0f") + strings.Count(l, "\ufe0e"); n != 0 {
			t.Fatalf("line %d still contains %d orphan selector(s): %q", i, n, l)
		}
		expanded := terminaltext.ExpandTabsANSI(l, terminaltext.TabWidth)
		if w := terminaltext.Width(expanded); w < cardWidth {
			t.Fatalf("line %d width = %d, want >= %d (card background would not reach the right edge):\n%q",
				i, w, cardWidth, l)
		}
	}
}

// End-to-end: the apply_patch error card shows the requested patch through the
// same highlighted preview path, so orphan-bearing patch text must be stripped
// before rendering too.
func TestApplyPatchErrorCardPreviewStripsOrphans(t *testing.T) {
	const cardWidth = 100
	patch := "*** Begin Patch\n*** Update File: src/sample.go\n@@\n-count is \ufe0f42\n+count is 42\n"
	args, err := json.Marshal(map[string]string{"patch": patch})
	if err != nil {
		t.Fatal(err)
	}
	b := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameApplyPatch,
		Content:       applyPatchToolDisplayArgs(string(args)),
		RawArgs:       string(args),
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "Error: hunk failed to apply",
	}

	for i, l := range b.Render(cardWidth, "") {
		if n := strings.Count(l, "\ufe0f") + strings.Count(l, "\ufe0e"); n != 0 {
			t.Fatalf("line %d still contains %d orphan selector(s): %q", i, n, l)
		}
		expanded := terminaltext.ExpandTabsANSI(l, terminaltext.TabWidth)
		if w := terminaltext.Width(expanded); w < cardWidth {
			t.Fatalf("line %d width = %d, want >= %d (card background would not reach the right edge):\n%q",
				i, w, cardWidth, l)
		}
	}
}

// The success-state diff renders file contents, which can legitimately carry
// orphaned selectors left by editors or earlier tool runs. Both the one-sided
// and paired diff line renderers must strip them before width math.
func TestUnifiedDiffLineStripsOrphanSelectors(t *testing.T) {
	hl := syntax.New("src/sample.go", "count is 42", "")
	var out []string
	appendApplyPatchToolUnifiedDiffLine(&out, "count is \ufe0f42", 1, 60, hl, false)
	appendApplyPatchToolUnifiedDiffPair(&out, "old value \ufe0f1", "new value 1", 1, 2, 60, hl)
	joined := strings.Join(out, "\n")
	if strings.ContainsRune(joined, '\ufe0f') || strings.ContainsRune(joined, '\ufe0e') {
		t.Fatalf("diff line renderers left an orphan selector: %q", joined)
	}
}
