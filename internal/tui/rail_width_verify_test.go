package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/tools"
)

// TestNoCardOverflowsTerminal is the regression guard for the off-by-one that
// made cards paint one column past the terminal width: the rail ("│") is
// prepended OUTSIDE the card surface, so its column must be reserved from the
// width budget. Before the fix a full-width card rendered width+1 columns, the
// terminal hard-wrapped it, and the trailing background column reappeared at
// the start of the next row (visible as a stray dark block on the right) while
// the rail column visually broke.
func TestNoCardOverflowsTerminal(t *testing.T) {
	ApplyTheme(DefaultTheme())
	patch := "*** Begin Patch\n*** Update File: internal/agent/main_llm_fallback.go\n@@\nimport (\n*** End Patch\n"

	cards := []struct {
		name string
		b    *Block
	}{
		{"user", &Block{ID: 1, Type: BlockUser, Content: "审查未 push 的 commits 是否正确合理"}},
		{"assistant", &Block{ID: 2, Type: BlockAssistant, Content: "hello world"}},
		{"thinking", &Block{ID: 3, Type: BlockThinking, Content: "Adjusting add-before context formatting"}},
		{"tool:apply_patch", &Block{ID: 677, Type: BlockToolCall, ToolName: tools.NameApplyPatch,
			Content:       fmt.Sprintf(`{"patch":%q}`, patch),
			ResultContent: "apply_patch failed: no changes were committed.",
			ResultDone:    true, ResultStatus: agent.ToolResultStatusError}},
		{"tool:edit", &Block{ID: 7, Type: BlockToolCall, ToolName: tools.NameEdit,
			Content: `{"path":"a.go"}`, Diff: "@@ -1,2 +1,2 @@\n package a\n-package old\n+package new\n", ResultDone: true}},
		{"tool:write", &Block{ID: 8, Type: BlockToolCall, ToolName: tools.NameWrite,
			Content: `{"path":"main.go","content":"package main"}`, ResultDone: true}},
		{"tool:read", &Block{ID: 9, Type: BlockToolCall, ToolName: tools.NameRead,
			Content:       `{"path":"internal/agent/main_policy_test.go","offset":1455,"limit":45}`,
			ResultContent: "1455\tline one\n1456\tline two", ResultDone: true}},
		{"tool:shell", &Block{ID: 10, Type: BlockToolCall, ToolName: tools.NameShell,
			Content:       `{"command":"go build ./... && go test ./internal/agent/ -run TestCompaction","description":"编译当前 compaction 与 fallback 修复","timeout":600}`,
			ResultContent: "ok  \tgithub.com/keakon/chord/internal/agent\t1.234s", ResultDone: true}},
		{"tool:grep", &Block{ID: 11, Type: BlockToolCall, ToolName: tools.NameGrep,
			Content: `{"pattern":"railWidthToReserve"}`, ResultContent: "a.go:1:match\nb.go:2:match", ResultDone: true}},
	}

	for _, w := range []int{40, 60, 80, 100, 120, 160, 200, 240, 290} {
		for _, c := range cards {
			for i, ln := range c.b.Render(w, "") {
				if dw := tuiStringWidth(ln); dw > w {
					t.Errorf("viewport %d: %s line %d renders %d cols (+%d past terminal)",
						w, c.name, i, dw, dw-w)
				}
			}
		}
	}
}

// TestRailGlyphIsLeftmost pins the rail contract: the "│" glyph is prepended at
// column 0 of every card line, outside the card background.
func TestRailGlyphIsLeftmost(t *testing.T) {
	ApplyTheme(DefaultTheme())
	b := &Block{ID: 5, Type: BlockThinking, Content: "Adjusting add-before context formatting"}
	if railANSISeq("thinking", false) == "" {
		t.Skip("thinking rail disabled in this theme")
	}
	lines := b.Render(100, "")
	found := 0
	for _, ln := range lines {
		plain := stripANSI(ln)
		if plain == "" {
			continue
		}
		if !strings.HasPrefix(plain, "│") {
			continue
		}
		found++
	}
	if found == 0 {
		t.Fatal("no rail-bearing line found; rail glyph missing from thinking card")
	}
	t.Logf("thinking card: %d rail-bearing lines, rail seq=%q", found, railANSISeq("thinking", false))
}
