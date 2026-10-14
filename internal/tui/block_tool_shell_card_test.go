package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/terminaltext"
)

func TestToolDisplayResultHidesModelFacingShellDurationNote(t *testing.T) {
	block := &Block{
		Type:          BlockToolCall,
		ToolName:      tools.NameShell,
		ResultContent: "done\n(command took 12.3s)",
		ResultDone:    true,
	}
	if got := toolDisplayResultContent(block); got != "done" {
		t.Fatalf("toolDisplayResultContent() = %q, want %q", got, "done")
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(rendered, "✓ ▸ shell · ⏱ 12s") {
		t.Fatalf("expected rendered shell card to show duration clock; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "\n⏱") || strings.Contains(rendered, "\n  ⏱") {
		t.Fatalf("expected rendered shell card to append duration inline; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "command took") {
		t.Fatalf("did not expect model-facing duration note in rendered card; got:\n%s", rendered)
	}
	if copied := blockCopyContent(block); !strings.Contains(copied, "(command took 12.3s)") {
		t.Fatalf("expected copied shell card to retain duration note; got:\n%s", copied)
	}
}

func TestShellCardStripsIgnoredArgNoteFromOutputSection(t *testing.T) {
	// The model reaches for a name the schema does not define (here the old
	// "yield_ms"), so the runtime drops it and appends a model-facing note. The
	// card surfaces that fact on the header instead of repainting the note as
	// command output.
	note := "Note: ignored unrecognized parameter(s): args.yield_ms"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               tools.NameShell,
		Content:                `{"command":"go test ./internal/tui/","yield_ms":240000}`,
		ResultContent:          "done\n(command took 14s)\n" + note,
		ResultNotes:            []string{note},
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"command":"go test ./internal/tui/","yield_ms":240000}`,
			EffectiveArgsJSON: `{"command":"go test ./internal/tui/"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.yield_ms",
				ValueJSON: "240000",
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(160, ""), "\n"))
	if !strings.Contains(rendered, "⏱ 14s") {
		t.Fatalf("expected whole-second duration on the header; got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "ignored yield_ms=240000") {
		t.Fatalf("expected the ignored argument on the header; got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "done") {
		t.Fatalf("expected command output to stay in the body; got:\n%s", rendered)
	}
	for _, unwanted := range []string{"command took", "Note:", "ignored unrecognized parameter(s)"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("did not expect %q in the rendered card; got:\n%s", unwanted, rendered)
		}
	}
	if copied := blockCopyContent(block); !strings.Contains(copied, note) || !strings.Contains(copied, "(command took 14s)") {
		t.Fatalf("expected copied shell card to retain the model-facing note and duration; got:\n%s", copied)
	}
}

func TestFailedShellCardStripsIgnoredArgNoteBeforeErrorTail(t *testing.T) {
	// A failed call appends "Error: …" after the runtime's notes, so the note
	// is not the tail of the result text. The card must still not repaint it as
	// command output: the argument audit on the header carries the fact.
	note := "Note: ignored unrecognized parameter(s): args.yield_ms"
	block := &Block{
		ID:                     2,
		Type:                   BlockToolCall,
		ToolName:               tools.NameShell,
		Content:                `{"command":"go build ./...","yield_ms":240000}`,
		ResultContent:          "out\n" + note + "\n\nError: exit code 1",
		ResultNotes:            []string{note},
		ResultDone:             true,
		ResultStatus:           message.ToolStatusError,
		ToolCallDetailExpanded: true,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"command":"go build ./...","yield_ms":240000}`,
			EffectiveArgsJSON: `{"command":"go build ./..."}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.yield_ms",
				ValueJSON: "240000",
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(160, ""), "\n"))
	if !strings.Contains(rendered, "ignored yield_ms=240000") {
		t.Fatalf("expected the ignored argument on the header; got:\n%s", rendered)
	}
	for _, want := range []string{"out", "exit code 1"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("expected %q in the failed card; got:\n%s", want, rendered)
		}
	}
	for _, unwanted := range []string{"Note:", "ignored unrecognized parameter(s)"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("did not expect %q in the rendered card; got:\n%s", unwanted, rendered)
		}
	}
	if copied := blockCopyContent(block); !strings.Contains(copied, note) {
		t.Fatalf("expected the copied card to retain the model-facing note; got:\n%s", copied)
	}
}

func TestRestoredShellToolCardShowsPersistedDuration(t *testing.T) {
	nextID := 0
	blocks := messagesToBlocks([]message.Message{
		{Role: message.RoleAssistant, ToolCalls: []message.ToolCall{{ID: "shell-restore", Name: tools.NameShell, Args: []byte(`{"command":"go test ./..."}`)}}},
		{Role: message.RoleTool, ToolCallID: "shell-restore", Content: "done", ToolStatus: string(message.ToolStatusSuccess), ToolDurationMs: 8700},
	}, &nextID)
	if len(blocks) != 1 {
		t.Fatalf("messagesToBlocks() returned %d blocks, want 1", len(blocks))
	}
	rendered := terminaltext.StripANSI(strings.Join(blocks[0].Render(80, ""), "\n"))
	if !strings.Contains(rendered, "⏱ 9s") {
		t.Fatalf("expected restored shell card to show duration clock; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "command took") {
		t.Fatalf("did not expect raw duration note in restored card; got:\n%s", rendered)
	}
}

func TestExpandedShellToolCardKeepsElapsedOnHeader(t *testing.T) {
	// Expanding a shell card must not move the elapsed label to the end of the
	// stdout body: it stays on the header line regardless of collapse state.
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               tools.NameShell,
		Content:                `{"command":"grep -n 'context' internal/agent/sub_routing_invalidated_test.go","timeout_ms":120000}`,
		ResultContent:          "---\n internal/agent/sub_routing_invalidated_test.go | 9 ++++++---\n1\tfile changed, 6 insertions(+), 3 deletions(-)\n---\n(command took 1.3s)",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}
	lines := strings.Split(terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n")), "\n")
	header := ""
	for _, line := range lines {
		if strings.Contains(line, "shell") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatal("expected rendered shell card to contain a header line")
	}
	if !strings.Contains(header, "· ⏱ 1s") {
		t.Fatalf("expected header to carry the elapsed label; header:\n%s\nfull:\n%s", header, strings.Join(lines, "\n"))
	}
	for _, line := range lines {
		if line == header {
			continue
		}
		if strings.Contains(line, "⏱") {
			t.Fatalf("expected elapsed only on the header, but found it on a body line:\n%s", line)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "↳ Output:") || !strings.Contains(strings.Join(lines, "\n"), "file changed, 6 insertions") {
		t.Fatalf("expected expanded stdout body; got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestCollapsedShellToolShowsExpandHintForHiddenOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"printf 'one\ntwo\nthree\n'","description":"show lines"}`,
		ResultContent:          "one\ntwo\nthree",
		ResultDone:             true,
		Collapsed:              true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "✓ ▸ shell show lines") {
		t.Fatalf("expected collapsed Shell to show its disclosure glyph and description; got:\n%s", joined)
	}
	for _, hidden := range []string{"Command:", "one", "two", "three", "more lines", "[space]"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("collapsed Shell should hide %q; got:\n%s", hidden, joined)
		}
	}
}

func TestCollapsedBashDoesNotDuplicateDescriptionInSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"ls /tmp","description":"List temp files"}`,
		ResultContent:          "file1\nfile2\nfile3",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	// Description "List temp files" should appear in the header line, not
	// repeated in the summary line.
	_, after, ok := strings.Cut(joined, "List temp files")
	if !ok {
		t.Fatalf("expected description in header; got:\n%s", joined)
	}
	// Check it does not appear a second time after the header.
	remainder := after
	if strings.Contains(remainder, "List temp files") {
		t.Fatalf("description should not be duplicated in summary; got:\n%s", joined)
	}
}

func TestCollapsedBashLongOutputStillFolds(t *testing.T) {
	var lines []string
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"cat big.txt","description":"show file"}`,
		ResultContent:          strings.Join(lines, "\n"),
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "✓ ▸ shell show file") {
		t.Fatalf("expected collapsed Shell to show the disclosure glyph; got:\n%s", joined)
	}
	if strings.Contains(joined, "line 1") || strings.Contains(joined, "line 8") {
		t.Fatalf("did not expect collapsed Shell to reveal output lines; got:\n%s", joined)
	}
}

func TestBashHeaderUsesOnlyFirstCommandLine(t *testing.T) {
	cmd := "echo first\necho second"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout_ms":120000}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := block.Render(80, "")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = terminaltext.StripANSI(line)
	}
	joined := strings.Join(plain, "\n")

	if !strings.Contains(joined, "shell echo first (timeout=2m)") {
		t.Fatalf("expected header to contain only first command line with timeout; got:\n%s", joined)
	}
	if strings.Contains(joined, "echo second (timeout=2m)") {
		t.Fatalf("did not expect command continuation to appear in collapsed header; got:\n%s", joined)
	}
}

func TestCollapsedBashMultilineUsesDescriptionWhenPresent(t *testing.T) {
	cmd := "python3 - <<'PY'\nfrom pathlib import Path\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":%q,"timeout_ms":120000}`, cmd, "Search existing permission-related tests"),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell Search existing permission-related tests (timeout=2m)") {
		t.Fatalf("expected collapsed multiline Shell header to use description; got:\n%s", joined)
	}
	if strings.Contains(joined, "Command:") || strings.Contains(joined, "python3 - <<'PY'") || strings.Contains(joined, "ok") {
		t.Fatalf("expected collapsed multiline Shell to hide command and output bodies; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsCappedForegroundTimeoutInHeader(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"sleep 1","timeout_ms":2400000}`,
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell sleep 1 (timeout=40m→10m)") {
		t.Fatalf("expected collapsed Shell header to show requested and effective capped foreground timeout; got:\n%s", joined)
	}
}

func TestCollapsedBashBackgroundTimeoutUsesLargerCapInHeader(t *testing.T) {
	cases := []struct {
		name      string
		timeoutMs int
		want      string
	}{
		{name: "hour-scale accepted for background", timeoutMs: 7200000, want: "shell sleep 1 (background, timeout=2h)"},
		{name: "explicit foreground default is a real deadline for background", timeoutMs: 600000, want: "shell sleep 1 (background, timeout=10m)"},
		{name: "beyond six hours capped", timeoutMs: 28800000, want: "shell sleep 1 (background, timeout=8h→6h)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := &Block{
				ID:                     1,
				Type:                   BlockToolCall,
				ToolName:               "shell",
				Content:                fmt.Sprintf(`{"command":"sleep 1","run_in_background":true,"timeout_ms":%d}`, tc.timeoutMs),
				ResultContent:          "ok",
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			}
			joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("expected collapsed Shell header to contain %q; got:\n%s", tc.want, joined)
			}
		})
	}
}

func TestCollapsedBashMultilineWithoutDescriptionFallsBackToCommand(t *testing.T) {
	cmd := "python3 - <<'PY'\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":"   ","timeout_ms":120000}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell python3 - <<'PY' (timeout=2m)") {
		t.Fatalf("expected collapsed multiline Shell header to fall back to command first line; got:\n%s", joined)
	}
}

func TestExpandedBashMultilineKeepsCommandHeaderEvenWithDescription(t *testing.T) {
	cmd := "python3 - <<'PY'\nfrom pathlib import Path\nprint('ok')\nPY"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":%q,"timeout_ms":120000}`, cmd, "Search existing permission-related tests"),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if !strings.Contains(joined, "shell Search existing permission-related tests (timeout=2m)") {
		t.Fatalf("expected expanded Shell header to use summary description; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") || !strings.Contains(joined, "python3 - <<'PY'") || !strings.Contains(joined, "from pathlib import Path") || !strings.Contains(joined, "print('ok')") {
		t.Fatalf("expected expanded Shell to show full command block; got:\n%s", joined)
	}
	// The description is the header's subject, so the body must not repeat it.
	if strings.Count(joined, "Search existing permission-related tests") != 1 {
		t.Fatalf("expected the description only on the header; got:\n%s", joined)
	}
}

func TestExpandedBashShowsIndentedContinuationLines(t *testing.T) {
	cmd := "echo first\necho second\n  pwd"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout_ms":120000}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	lines := block.Render(80, "")
	var plain []string
	for _, line := range lines {
		plain = append(plain, terminaltext.StripANSI(line))
	}

	foundContinuation := false
	foundIndentedContinuation := false
	foundIndentedNested := false
	for _, line := range plain {
		// Strip rail prefix if present
		content := line
		if strings.HasPrefix(content, "│\x1b[0m") {
			content = content[len("│\x1b[0m"):]
		} else if strings.HasPrefix(content, "│") {
			content = content[len("│"):]
		}
		if strings.Contains(content, "echo second") {
			foundContinuation = true
			if strings.HasPrefix(content, "      echo second") {
				foundIndentedContinuation = true
			}
		}
		if strings.Contains(content, "pwd") && strings.HasPrefix(content, "        pwd") {
			foundIndentedNested = true
		}
	}
	if !foundContinuation {
		t.Fatalf("expected expanded Shell view to include continuation lines; got:\n%s", strings.Join(plain, "\n"))
	}
	if !foundIndentedContinuation {
		t.Fatalf("expected expanded Shell continuation line to be indented; got:\n%s", strings.Join(plain, "\n"))
	}
	if !foundIndentedNested {
		t.Fatalf("expected expanded Shell nested continuation line to preserve extra indentation; got:\n%s", strings.Join(plain, "\n"))
	}
}

func TestCollapsedBashShowsCommandPreviewAndExpandHint(t *testing.T) {
	cmd := "echo first\necho second\necho third"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout_ms":120000}`, cmd),
		ResultContent:          "ok",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(joined, "✓ ▸ shell echo first (timeout=2m)") {
		t.Fatalf("expected collapsed Shell to show its first-line command summary; got:\n%s", joined)
	}
	for _, hidden := range []string{"Command:", "echo second", "echo third", "ok", "[space]"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("collapsed Shell should hide %q; got:\n%s", hidden, joined)
		}
	}
}

func TestCollapsedBashLongCommandWithNoOutputKeepsCommandPreviewCollapsed(t *testing.T) {
	cmd := "git add internal/tui/app_cached_render.go && cat <<'PATCH' | git apply --cached\n" +
		"--- a/internal/tui/session_switch_test.go\n" +
		"+++ b/internal/tui/session_switch_test.go\n" +
		"@@ -1,2 +1,8 @@\n" +
		"+func TestExample(t *testing.T) {\n" +
		"+\tt.Fatal(\"one\")\n" +
		"+\tt.Fatal(\"two\")\n" +
		"+}\n" +
		"PATCH"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"description":"暂存滚轮修复相关改动","timeout_ms":30000}`, cmd),
		ResultContent:          "(Shell completed with no output)",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "✓ ▸ shell 暂存滚轮修复相关改动 (timeout=30s)") {
		t.Fatalf("expected collapsed Shell to keep only the description summary; got:\n%s", joined)
	}
	for _, hidden := range []string{"Command:", "git add internal/tui/app_cached_render.go", "session_switch_test.go", "TestExample", "Shell completed", "[space]"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("collapsed Shell should hide %q; got:\n%s", hidden, joined)
		}
	}
}

func TestCollapsedBashShowsSingleExpandHintWhenCommandAndOutputBothHidden(t *testing.T) {
	cmd := "echo first\necho second\necho third"
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":%q,"timeout_ms":120000}`, cmd),
		ResultContent:          "one\ntwo\nthree",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(joined, "✓ ▸ shell echo first (timeout=2m)") {
		t.Fatalf("expected collapsed Shell to show its disclosure glyph; got:\n%s", joined)
	}
	for _, hidden := range []string{"Command:", "echo second", "echo third", "one", "two", "three", "[space]"} {
		if strings.Contains(joined, hidden) {
			t.Fatalf("collapsed Shell should hide %q; got:\n%s", hidden, joined)
		}
	}
}

func TestExpandedUserTerminalMatchesBashContinuationFormatting(t *testing.T) {
	cmd := "echo first\necho second\n  pwd"
	block := &Block{
		ID:                   1,
		Type:                 BlockUser,
		UserLocalShellCmd:    cmd,
		UserLocalShellResult: "ok",
		Collapsed:            false,
	}

	lines := block.Render(80, "")
	var plain []string
	for _, line := range lines {
		plain = append(plain, terminaltext.StripANSI(line))
	}
	joined := strings.Join(plain, "\n")

	if !strings.Contains(joined, "shell echo first") {
		t.Fatalf("expected terminal header to show first command line only; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Command:") {
		t.Fatalf("expected terminal to show command block label; got:\n%s", joined)
	}
	if !strings.Contains(joined, "      echo second") {
		t.Fatalf("expected terminal continuation line to be indented; got:\n%s", joined)
	}
	if !strings.Contains(joined, "        pwd") {
		t.Fatalf("expected terminal nested continuation indentation to be preserved; got:\n%s", joined)
	}
}

func TestBashCommandAndCollapsedSummaryEscapeANSIRichText(t *testing.T) {
	block := &Block{
		ID:        1,
		Type:      BlockToolCall,
		ToolName:  "shell",
		Content:   `{"command":"printf '\u001b[32mok\u001b[0m'","description":"\u001b[36mdesc\u001b[0m"}`,
		Collapsed: true,
		ResultContent: strings.Join([]string{
			"\x1b[31mline-1\x1b[0m",
			"line-2",
			"line-3",
			"line-4",
			"line-5",
			"line-6",
		}, "\n"),
		ResultDone: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected shell card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{`\x1b[36mdesc\x1b[0m`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected shell card to contain %q, got:\n%s", want, joined)
		}
	}
}

func TestCollapsedLargeBashResultDoesNotRenderEntireHiddenOutput(t *testing.T) {
	lines := make([]string, 50000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%05d", i)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "shell",
		Content:       `{"command":"cat huge.log","description":"show huge log"}`,
		Collapsed:     true,
		ResultContent: strings.Join(lines, "\n"),
		ResultDone:    true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "✓ ▸ shell show huge log") {
		t.Fatalf("expected collapsed Shell to show only its compact header, got:\n%s", joined)
	}
	if strings.Contains(joined, "line-00000") || strings.Contains(joined, "line-49999") || strings.Contains(joined, "more lines") {
		t.Fatalf("collapsed Shell should not render any output body or line-count hint, got:\n%s", joined)
	}
}

func TestCollapsedBashErrorShowsCrossPrefixAndRedOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"false"}`,
		ResultContent:          "stdout\n\nError: exit code 1",
		ResultStatus:           message.ToolStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := block.Render(80, "")
	joinedANSI := strings.Join(lines, "\n")
	joinedPlain := terminaltext.StripANSI(joinedANSI)

	if !strings.Contains(joinedPlain, "✗ ▸ shell false") {
		t.Fatalf("expected collapsed Shell error prefix; got:\n%s", joinedPlain)
	}
	if !strings.Contains(joinedPlain, "exit code 1") {
		t.Fatalf("expected collapsed Shell error summary; got:\n%s", joinedPlain)
	}
	if !strings.Contains(joinedANSI, "\x1b[1;38;5;"+currentStyles.Palette.ErrorFg+"m") && !strings.Contains(joinedANSI, "\x1b[38;5;"+currentStyles.Palette.ErrorFg+"m") {
		t.Fatalf("expected error styling ANSI sequence; got:\n%q", joinedANSI)
	}
}

func TestCollapsedBashRejectedShowsExpandHintBeforeRejection(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"first command line\nsecond command line\nthird command line","timeout_ms":120000}`,
		ResultContent:          `tool "shell" rejected by user: sample rejection reason`,
		ResultStatus:           message.ToolStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	lines := stripANSILines(block.Render(120, ""))
	rejectedIdx := -1
	for i, line := range lines {
		if strings.Contains(line, `tool "shell" rejected by user: sample rejection reason`) {
			rejectedIdx = i
		}
	}
	if rejectedIdx < 0 {
		t.Fatalf("expected collapsed Shell rejection to show rejection reason; got:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(strings.Join(lines, "\n"), "[space]") {
		t.Fatalf("collapsed Shell rejection should not show an inline disclosure footer; got:\n%s", strings.Join(lines, "\n"))
	}
}

func TestCollapsedBashMultiLineErrorShowsOnlyExitCode(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"go test ./internal/tui"}`,
		ResultContent:          "--- FAIL: TestFoo (0.00s)\n    app_test.go:12: boom\n\nError: exit code 1. Test or verification command failed. Inspect the first relevant failure; before rerunning a broad test, prefer a focused reproduction for the affected package/test",
		ResultStatus:           message.ToolStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "exit code 1") {
		t.Fatalf("expected collapsed multi-line Shell error to keep the exit code; got:\n%s", joined)
	}
	if strings.Contains(joined, "TestFoo") || strings.Contains(joined, "app_test.go:12") {
		t.Fatalf("collapsed multi-line Shell error must not guess a failure detail line; got:\n%s", joined)
	}
	if strings.Contains(joined, "Inspect the first relevant failure") {
		t.Fatalf("collapsed multi-line Shell error must not surface the guidance tail; got:\n%s", joined)
	}
}

func TestCollapsedBashSingleLineErrorShowsVerbatimLine(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"go build ./..."}`,
		ResultContent:          "Error: exit code 127: command not found: sample-tool",
		ResultStatus:           message.ToolStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "exit code 127: command not found: sample-tool") {
		t.Fatalf("expected collapsed single-line Shell error to show the whole line; got:\n%s", joined)
	}
}

func TestCollapsedBashTimeoutShowsTimeoutStatus(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"go test ./internal/tui"}`,
		ResultContent:          "some output line\n\ncommand timed out after 600s after output:\nmore output",
		ResultStatus:           message.ToolStatusError,
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "timed out after 600s") {
		t.Fatalf("expected collapsed Shell timeout to show the timeout status; got:\n%s", joined)
	}
	if strings.Contains(joined, "more output") {
		t.Fatalf("collapsed Shell timeout must not show the output tail; got:\n%s", joined)
	}
}

func TestExpandedBashErrorKeepsToolCardBackgroundAcrossWrappedErrorBody(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"cd /home/user/projects/sample-repo && git ls-files -s | grep '\\u0000' || true","description":"No-op check for nulls in git index","timeout":60,"workdir":""}`,
		ResultContent:          "exit code 1 after output:\nstarting command: fork/exec /bin/bash: invalid argument",
		ResultDone:             true,
		IsError:                true,
		ToolCallDetailExpanded: true,
	}

	lines := block.Render(100, "")
	target := -1
	for i, line := range lines {
		if strings.Contains(terminaltext.StripANSI(line), "starting command: fork/exec /bin/bash: invalid argument") {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("did not find wrapped shell error body in rendered lines:\n%s", strings.Join(stripANSILines(lines), "\n"))
	}

	line := lines[target]
	trimmed := strings.TrimRight(terminaltext.StripANSI(line), " ")
	contentEndCol := ansi.StringWidth(trimmed) - 1
	if contentEndCol < 0 {
		t.Fatalf("invalid content end col: %d", contentEndCol)
	}

	buf := newScreenBuffer(ansi.StringWidth(line), 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	toolBg := colorOfTheme(currentStyles.Palette.ToolCallBg)
	checked := 0
	cardEnd := len(cells) - currentStyles.ToolBlockStyle.GetMarginRight()
	for i := contentEndCol + 1; i < cardEnd; i++ {
		cell := cells[i]
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		checked++
		if !colorsEqual(cell.Style.Bg, toolBg) {
			t.Fatalf("trailing space at col %d background = %v, want tool card bg %v", i, cell.Style.Bg, toolBg)
		}
	}
	if checked == 0 {
		t.Fatal("expected trailing padding spaces on shell error line")
	}
}

func TestBashExpandedMetaShowsRelativeWorkdirInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	workdir := filepath.Join(wd, "internal", "tui")
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                fmt.Sprintf(`{"command":"pwd","workdir":%q}`, workdir),
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		displayWorkingDir:      wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "workdir: internal/tui") && !strings.Contains(joined, "Workdir: internal/tui") {
		t.Fatalf("expected shell expanded body to show relative workdir; got:\n%s", joined)
	}
	if strings.Contains(joined, workdir) {
		t.Fatalf("did not expect bash body to show absolute workdir; got:\n%s", joined)
	}
}

func TestCollapsedBashShowsResultSummary(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"go test ./internal/tui/..."}`,
		ResultContent:          "ok\nsecond line",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	if strings.Contains(joined, "ok") || strings.Contains(joined, "second line") {
		t.Fatalf("expected shell collapsed card to hide success output body; got:\n%s", joined)
	}
	if !strings.Contains(joined, "✓ ▸ shell go test ./internal/tui/...") {
		t.Fatalf("expected shell collapsed card to retain its disclosure glyph; got:\n%s", joined)
	}
}

func TestBashResultEscapesANSIRichOutput(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "shell",
		Content:                `{"command":"printf test"}`,
		ResultContent:          "\x1b[31mred\x1b[0m\nplain",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected rendered Shell card to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[31mred\x1b[0m`) {
		t.Fatalf("expected shell output to show ANSI literally, got:\n%s", joined)
	}
	if !strings.Contains(joined, "plain") {
		t.Fatalf("expected shell output to keep plain text, got:\n%s", joined)
	}
}

func TestTerminalResultEscapesANSIRichOutput(t *testing.T) {
	block := &Block{
		ID:                   1,
		Type:                 BlockUser,
		UserLocalShellCmd:    "printf demo",
		UserLocalShellResult: "\x1b[32mok\x1b[0m",
		Collapsed:            false,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected terminal card to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[32mok\x1b[0m`) {
		t.Fatalf("expected terminal result to show ANSI literally, got:\n%s", joined)
	}
}
