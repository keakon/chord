package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/keakon/lipgloss/v2"
	uv "github.com/keakon/ultraviolet"
	"github.com/keakon/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/keakon/chord/internal/agent"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/syntax"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

var osc8RegexToolTest = regexp.MustCompile(`\x1b\]8;[^\x07\x1b]*(?:\x07|\x1b\\)`)

func stripOSC8ToolTest(s string) string {
	return osc8RegexToolTest.ReplaceAllString(s, "")
}

func containsRawCarriageReturnForTest(s string) bool {
	for _, r := range s {
		if r == '\r' {
			return true
		}
	}
	return false
}

func TestGenericToolHeaderPartsShapeUnknownTools(t *testing.T) {
	// An MCP tool has no dedicated header entry, so the generic rule shapes
	// it: the conventional subject key loses its label and leads, the other
	// short scalars keep theirs in the option group, and structured values
	// drop to the body instead of being flattened onto the header.
	keys, vals := parseToolArgs(`{"numResults":8,"query":"file path typo suggestion ranking","maxToken":4096,"tokenCount":12,"temperature":0.7,"filters":{"language":"go"},"urls":["a","b"]}`)
	mainPart, grayPart, bodyKeys := genericToolHeaderParts(keys, vals)
	if mainPart != "file path typo suggestion ranking" {
		t.Fatalf("mainPart = %q", mainPart)
	}
	if grayPart != "(numResults=8, maxToken=4096, tokenCount=12, temperature=0.7)" {
		t.Fatalf("grayPart = %q", grayPart)
	}
	if want := []string{"filters", "urls"}; !slices.Equal(bodyKeys, want) {
		t.Fatalf("bodyKeys = %v, want %v", bodyKeys, want)
	}
}

func TestGenericToolHeaderPartsShowModelArguments(t *testing.T) {
	keys, vals := parseToolArgs(`{"query":"search","apiKey":"model-supplied-value","filters":{"language":"go"},"urls":["a","b"]}`)
	mainPart, grayPart, bodyKeys := genericToolHeaderParts(keys, vals)
	if mainPart != "search" {
		t.Fatalf("mainPart = %q", mainPart)
	}
	if !strings.Contains(grayPart, "apiKey=model-supplied-value") {
		t.Fatalf("grayPart = %q", grayPart)
	}
	if want := []string{"filters", "urls"}; !slices.Equal(bodyKeys, want) {
		t.Fatalf("bodyKeys = %v, want %v", bodyKeys, want)
	}
}

func TestGenericToolHeaderKeepsLongOptionsOffTheHeader(t *testing.T) {
	// A long value under a non-subject key is content, not an option: it drops
	// to the body rather than stretching the header line.
	long := strings.Repeat("abcdefghij", 13)
	keys, vals := parseToolArgs(fmt.Sprintf(`{"query":"search","note":%q}`, long))
	mainPart, grayPart, bodyKeys := genericToolHeaderParts(keys, vals)
	if mainPart != "search" || grayPart != "" {
		t.Fatalf("mainPart = %q grayPart = %q", mainPart, grayPart)
	}
	if want := []string{"note"}; !slices.Equal(bodyKeys, want) {
		t.Fatalf("bodyKeys = %v, want %v", bodyKeys, want)
	}
}

func TestGenericToolHeaderPreviewsFirstBodyArgumentWhenNoSubjectMatches(t *testing.T) {
	// An imported Codex exec call arrives as an unknown tool whose only
	// argument is a multi-line script: no subject key matches, so the script's
	// opening line leads the header the way a shell card previews a multi-line
	// command. The key stays in bodyKeys so an expanded card still shows the
	// script in full.
	keys, vals := parseToolArgs(`{"input":"const r = await tools.write_stdin({session_id:481});\ntext(r.output);\n"}`)
	mainPart, grayPart, bodyKeys := genericToolHeaderParts(keys, vals)
	if mainPart != "const r = await tools.write_stdin({session_id:481});" {
		t.Fatalf("mainPart = %q", mainPart)
	}
	if grayPart != "" {
		t.Fatalf("grayPart = %q", grayPart)
	}
	if want := []string{"input"}; !slices.Equal(bodyKeys, want) {
		t.Fatalf("bodyKeys = %v, want %v", bodyKeys, want)
	}
}

func TestGenericToolHeaderSubjectKeyStillWinsOverBodyPreview(t *testing.T) {
	// A conventional subject key outranks the body preview: the multi-line
	// value stays in the body instead of competing for the header.
	keys, vals := parseToolArgs(`{"query":"search","input":"line one\nline two\n"}`)
	mainPart, grayPart, bodyKeys := genericToolHeaderParts(keys, vals)
	if mainPart != "search" {
		t.Fatalf("mainPart = %q, want the subject", mainPart)
	}
	if grayPart != "" {
		t.Fatalf("grayPart = %q", grayPart)
	}
	if want := []string{"input"}; !slices.Equal(bodyKeys, want) {
		t.Fatalf("bodyKeys = %v, want %v", bodyKeys, want)
	}
}

func TestGenericToolHeaderPreviewsFirstBodyArgumentInOrder(t *testing.T) {
	// With several body-bound arguments and no subject key, the first one in
	// argument order leads the header; the rest keep their body sections so an
	// expanded card still shows every value in full.
	keys, vals := parseToolArgs(`{"input":"line one\nline two\n","patch":"*** Begin Patch\n*** End Patch"}`)
	mainPart, grayPart, bodyKeys := genericToolHeaderParts(keys, vals)
	if mainPart != "line one" {
		t.Fatalf("mainPart = %q", mainPart)
	}
	if grayPart != "" {
		t.Fatalf("grayPart = %q", grayPart)
	}
	if want := []string{"input", "patch"}; !slices.Equal(bodyKeys, want) {
		t.Fatalf("bodyKeys = %v, want %v", bodyKeys, want)
	}
}

func contrastRatio(a, b color.Color) float64 {
	al := relativeLuminance(a)
	bl := relativeLuminance(b)
	if al < bl {
		al, bl = bl, al
	}
	return (al + 0.05) / (bl + 0.05)
}

func relativeLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	linear := func(v uint32) float64 {
		n := float64(v) / 0xffff
		if n <= 0.04045 {
			return n / 12.92
		}
		return math.Pow((n+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

func TestHighlightedFileToolsUseExpectedBackgrounds(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := "package main"
	readArgs, _ := json.Marshal(map[string]any{"path": "main.go", "offset": 15, "limit": 1})
	writeArgs, _ := json.Marshal(map[string]string{"path": "main.go", "content": content})
	diffArgs, _ := json.Marshal(map[string]string{"path": "main.go"})
	diff := "@@ -1,2 +1,2 @@\n package context\n-package old\n+package main\n"

	tests := []struct {
		name           string
		block          *Block
		codeBackground string
	}{
		{name: tools.NameRead, block: &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameRead, Content: string(readArgs), ResultContent: content, ResultDone: true}, codeBackground: currentStyles.Palette.ToolCallBg},
		{name: tools.NameWrite, block: &Block{ID: 2, Type: BlockToolCall, ToolName: tools.NameWrite, Content: string(writeArgs), ResultDone: true}, codeBackground: currentStyles.Palette.ToolCallBg},
		{name: tools.NameEdit, block: &Block{ID: 3, Type: BlockToolCall, ToolName: tools.NameEdit, Content: string(diffArgs), Diff: diff, ResultDone: true}, codeBackground: currentStyles.Palette.DiffAddLineBg},
		{name: tools.NameApplyPatch, block: &Block{ID: 4, Type: BlockToolCall, ToolName: tools.NameApplyPatch, Content: string(diffArgs), Diff: diff, ResultDone: true}, codeBackground: currentStyles.Palette.DiffAddLineBg},
	}
	toolBackground := colorOfTheme(currentStyles.Palette.ToolCallBg)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := tt.block.Render(80, "")
			line := renderedLineContaining(t, lines, content)
			assertRenderedTextBackground(t, line, "package", colorOfTheme(tt.codeBackground))
			if tt.name == tools.NameEdit || tt.name == tools.NameApplyPatch {
				contextLine := renderedLineContaining(t, lines, "package context")
				assertRenderedTextBackground(t, contextLine, "package", toolBackground)
			}
		})
	}
}

func renderedLineContaining(t *testing.T, lines []string, text string) string {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(terminaltext.StripANSI(line), text) {
			return line
		}
	}
	t.Fatalf("rendered output does not contain %q:\n%s", text, terminaltext.StripANSI(strings.Join(lines, "\n")))
	return ""
}

func assertRenderedTextBackground(t *testing.T, line, text string, want color.Color) {
	t.Helper()
	for _, cell := range renderedCellsForText(t, line, text) {
		if !colorsEqual(cell.Style.Bg, want) {
			t.Fatalf("cell %q background = %v, want %v", cell.Content, cell.Style.Bg, want)
		}
	}
}

func assertRenderedTextForeground(t *testing.T, line, text string, want color.Color) {
	t.Helper()
	for _, cell := range renderedCellsForText(t, line, text) {
		if !colorsEqual(cell.Style.Fg, want) {
			t.Fatalf("cell %q foreground = %v, want %v", cell.Content, cell.Style.Fg, want)
		}
	}
}

func renderedCellsForText(t *testing.T, line, text string) []uv.Cell {
	t.Helper()
	plain := terminaltext.StripANSI(line)
	byteStart := strings.Index(plain, text)
	if byteStart < 0 {
		t.Fatalf("line does not contain %q: %q", text, plain)
	}
	byteEnd := byteStart + len(text)
	buf := newScreenBuffer(ansi.StringWidth(line), 1)
	uv.NewStyledString(line).Draw(buf, buf.Bounds())
	var matched []uv.Cell
	pos := 0
	for _, cell := range buf.Line(0) {
		if cell.Content == "" {
			continue
		}
		next := pos + len(cell.Content)
		if pos < byteEnd && next > byteStart {
			matched = append(matched, cell)
		}
		pos = next
	}
	if len(matched) == 0 {
		t.Fatalf("no rendered cells found for %q in %q", text, plain)
	}
	return matched
}

func TestDisplayToolPathRelativizesHomePathUnderWorkingDir(t *testing.T) {
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("Setenv HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.Setenv("HOME", oldHome) })

	workingDir := filepath.Join(home, "Workspace", "repo")
	absPath := filepath.Join(workingDir, "internal", "tools", "path_resolution.go")
	input := "~/Workspace/repo/internal/tools/path_resolution.go"
	if got := displayToolPath(input, workingDir); got != "internal/tools/path_resolution.go" {
		t.Fatalf("displayToolPath(%q) = %q", input, got)
	}
	if got := displayToolPath(absPath, workingDir); got != "internal/tools/path_resolution.go" {
		t.Fatalf("displayToolPath(abs) = %q", got)
	}
}

func TestToolHeaderProgressSuffixSurvivesANSITruncateFallback(t *testing.T) {
	ApplyTheme(uitheme.Default())
	header := "🔧 \x1b[1mVeryLongToolNameWithArguments\x1b[0m /tmp/very/long/path"
	got := appendToolProgressSuffix(header, &agent.ToolProgressSnapshot{Text: "42 chars received"}, 24)
	plain := terminaltext.StripANSI(got)
	if !strings.Contains(plain, "42 chars received") {
		t.Fatalf("progress suffix missing after truncate fallback: %q", plain)
	}
	if runewidth.StringWidth(plain) > 24 {
		t.Fatalf("header width = %d, want <= 24: %q", runewidth.StringWidth(plain), plain)
	}
}

func TestDisplayToolPathPrefersRelativeWithinWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "block_tool.go")
	got := displayToolPath(abs, wd)
	want := filepath.Join("internal", "tui", "block_tool.go")
	if got != want {
		t.Fatalf("displayToolPath(%q, %q) = %q, want %q", abs, wd, got, want)
	}
}

func TestDisplayToolPathKeepsAbsoluteOutsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(string(os.PathSeparator), "tmp", "other", "block_tool.go")
	if got := displayToolPath(abs, wd); got != abs {
		t.Fatalf("displayToolPath(%q, %q) = %q, want original absolute path", abs, wd, got)
	}
}

// The model may reach the working dir by another spelling than the session's:
// through a linked directory, or the other way round (macOS /tmp is
// /private/tmp). Either way a file inside it is shown relative to it.
func TestDisplayToolPathFollowsSymlinkedWorkingDir(t *testing.T) {
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	outside := t.TempDir()
	want := filepath.Join("src", "app.go")
	for _, tc := range []struct {
		name, path, workingDir, want string
	}{
		{"path through the link", filepath.Join(link, "src", "app.go"), real, want},
		{"working dir through the link", filepath.Join(real, "src", "app.go"), link, want},
		{"deleted file through the link", filepath.Join(link, "gone", "old.sh"), real, filepath.Join("gone", "old.sh")},
		{"outside the working dir", filepath.Join(outside, "app.go"), link, filepath.Join(outside, "app.go")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := displayToolPath(tc.path, tc.workingDir); got != tc.want {
				t.Fatalf("displayToolPath(%q, %q) = %q, want %q", tc.path, tc.workingDir, got, tc.want)
			}
		})
	}
}

func TestDisplayToolPathKeepsExistingRelativePath(t *testing.T) {
	rel := filepath.Join("internal", "tui", "block_tool.go")
	if got := displayToolPath(rel, filepath.Join(string(os.PathSeparator), "tmp", "workspace")); got != rel {
		t.Fatalf("displayToolPath(%q, wd) = %q, want unchanged relative path", rel, got)
	}
}

func TestGenericToolCardStripsIgnoredArgNoteFromResult(t *testing.T) {
	// Tools without a dedicated renderer echo the whole result as the body, so
	// the runtime's note must not be painted as that tool's output.
	note := "Note: ignored unrecognized parameter(s): args.verbose"
	block := &Block{
		ID:                     4,
		Type:                   BlockToolCall,
		ToolName:               "mcp__sample__ping",
		Content:                `{"target":"example.invalid","verbose":true}`,
		ResultContent:          "pong\n" + note,
		ResultNotes:            []string{note},
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"target":"example.invalid","verbose":true}`,
			EffectiveArgsJSON: `{"target":"example.invalid"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.verbose",
				ValueJSON: "true",
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(160, ""), "\n"))
	if !strings.Contains(rendered, "ignored verbose=true") {
		t.Fatalf("expected the ignored argument on the header; got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "pong") {
		t.Fatalf("expected the tool output in the body; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "Note:") {
		t.Fatalf("did not expect the runtime note in the result body; got:\n%s", rendered)
	}
}

func TestStripResultNotesRemovesOnlyRecordedRuntimeNotes(t *testing.T) {
	noteA := "Note: ignored unrecognized parameter(s): args.yield_ms"
	noteB := "Note: ignored null parameter(s): args.timeout_ms; treated as unset, so pass a value or omit the parameter"
	withNotes := &Block{ResultNotes: []string{noteA, noteB}}

	cases := []struct {
		name string
		b    *Block
		in   string
		want string
	}{
		{"notes before an error tail", withNotes, "out\n" + noteA + "\n\nError: exit code 1", "out\n\nError: exit code 1"},
		{"notes at the tail", withNotes, "out\n" + noteA + "\n" + noteB, "out"},
		{"crlf line endings", withNotes, "out\r\n" + noteA + "\r\n", "out"},
		{"unrecorded note-like line", withNotes, "out\nNote: ignored unrecognized parameter(s): args.other", "out\nNote: ignored unrecognized parameter(s): args.other"},
		{"no notes recorded", &Block{}, "out\n" + noteA, "out\n" + noteA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.stripResultNotes(tc.in); got != tc.want {
				t.Fatalf("stripResultNotes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestToolElapsedHiddenBelowOneSecond(t *testing.T) {
	block := &Block{
		Type:       BlockToolCall,
		ToolName:   tools.NameShell,
		ResultDone: true,
		StartedAt:  time.Unix(0, 0),
		SettledAt:  time.Unix(0, int64(900*time.Millisecond)),
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if strings.Contains(rendered, "⏱") {
		t.Fatalf("expected sub-second tool elapsed to stay hidden; got:\n%s", rendered)
	}
}

func TestSkillToolDisplaySummaryUsesFullNameAndRelativeDirectoryPath(t *testing.T) {
	tmp := t.TempDir()
	wd := filepath.Join(tmp, "workspace")
	if err := os.MkdirAll(filepath.Join(wd, ".agents", "skills", "sample-food-skill"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(prevWD) }()

	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "skill",
		Content:       `{"name":"sample-food-skill","result":"<path>` + filepath.ToSlash(filepath.Join(wd, ".agents", "skills", "sample-food-skill", "SKILL.md")) + `</path>"}`,
		ResultContent: filepath.ToSlash(filepath.Join(wd, ".agents", "skills", "sample-food-skill", "SKILL.md")),
		ResultDone:    true,
		Collapsed:     true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(140, ""), "\n"))
	if !strings.Contains(joined, "skill sample-food-skill (from .agents)") {
		t.Fatalf("expected skill header to show name with source metadata, got:\n%s", joined)
	}
	if strings.Contains(joined, "sample-food-…") {
		t.Fatalf("skill name should not be truncated before source metadata, got:\n%s", joined)
	}
	if strings.Contains(joined, "name:") {
		t.Fatalf("collapsed skill header should not repeat a name param line, got:\n%s", joined)
	}
}

func TestOrdinaryToolResultUsesPlainTextForMarkdownLookingContent(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://example.com"}`,
		ResultContent:          "## Ready\n\n```go\nfmt.Println(1)\n```\n\n- item one\n- item two",
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"## Ready", "```go", "fmt.Println(1)", "- item one", "- item two"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected markdown-looking tool result content to remain literal (%q), got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• item one") {
		t.Fatalf("ordinary tool result must not render markdown bullets, got:\n%s", joined)
	}
	if strings.Contains(joined, "GO") {
		t.Fatalf("ordinary tool result must not use assistant-style fenced code rendering, got:\n%s", joined)
	}
}

func TestExpandedSkillResultUsesPlainTextForMarkdownLookingContent(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "skill",
		Content:                `{"name":"skill-creator","result":"<path>/tmp/skills/skill-creator/SKILL.md</path>"}`,
		ResultContent:          "<skill>\n<name>skill-creator</name>\n<path>/tmp/skills/skill-creator/SKILL.md</path>\n<root>/tmp/skills/skill-creator</root>\n\n# Skill Creator\n\n- Step one\n- Step two\n</skill>",
		ResultDone:             true,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"# Skill Creator", "- Step one", "- Step two"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected expanded Skill result to keep literal markdown-looking text %q, got:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "• Step one") {
		t.Fatalf("expanded Skill result should not render markdown bullets, got:\n%s", joined)
	}
}

func TestOrdinaryToolResultDoesNotUseRichMarkdownFencedCode(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://example.com"}`,
		ResultContent:          "## Ready\n\n```go\nfmt.Println(1)\n```",
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, "Ready") || !strings.Contains(joined, "fmt.Println(1)") {
		t.Fatalf("expected markdown-looking tool result content to remain visible, got:\n%s", joined)
	}
	if strings.Contains(joined, "GO") {
		t.Fatalf("ordinary tool result must not use assistant-style rich fenced code rendering, got:\n%s", joined)
	}
}

func TestToolProgressRendersConsistentlyAcrossCollapsedAndExpandedStates(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "delete",
		Content:            `{"paths":["a-very-long-file-name.txt","b-very-long-file-name.txt","c-very-long-file-name.txt"],"reason":"cleanup stale generated benchmark artifacts"}`,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
		ToolProgress: &agent.ToolProgressSnapshot{
			Label:   "paths",
			Current: 2,
			Total:   3,
		},
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(44, "●"), "\n"))
	if !strings.Contains(collapsed, "2 / 3 paths") {
		t.Fatalf("collapsed render should preserve tool progress; got:\n%s", collapsed)
	}

	block.Collapsed = false
	expanded := terminaltext.StripANSI(strings.Join(block.Render(44, "●"), "\n"))
	if !strings.Contains(expanded, "2 / 3 paths") {
		t.Fatalf("expanded render should preserve tool progress; got:\n%s", expanded)
	}
}

func TestToolResultWrappedContinuationKeepsCardBackgroundOnTrailingPadding(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:         1,
		Type:       BlockToolResult,
		ToolName:   "shell",
		Content:    "Error: " + strings.Repeat("x", 220),
		IsError:    true,
		Collapsed:  false,
		ResultDone: true,
	}

	lines := block.Render(88, "")
	if len(lines) == 0 {
		t.Fatal("expected rendered tool result block")
	}

	var wrapped []string
	for _, line := range lines {
		plain := terminaltext.StripANSI(line)
		if strings.Contains(plain, strings.Repeat("x", 20)) {
			wrapped = append(wrapped, line)
		}
	}
	if len(wrapped) < 2 {
		t.Fatalf("expected at least 2 wrapped content lines, got %d: %q", len(wrapped), strings.Join(lines, "\n"))
	}
	target := wrapped[1]

	plain := terminaltext.StripANSI(target)
	trimmed := strings.TrimRight(plain, " ")
	if trimmed == plain {
		t.Fatalf("wrapped continuation has no trailing spaces: %q", plain)
	}
	contentEndCol := ansi.StringWidth(trimmed) - 1
	if contentEndCol < 0 {
		t.Fatalf("invalid content end col: %d", contentEndCol)
	}

	buf := newScreenBuffer(ansi.StringWidth(target), 1)
	uv.NewStyledString(target).Draw(buf, buf.Bounds())
	cells := buf.Line(0)
	if len(cells) == 0 {
		t.Fatal("expected rendered cells")
	}

	toolBg := colorOfTheme(currentStyles.Palette.ToolCallBg)
	trailingSpaces := 0
	cardEnd := len(cells) - currentStyles.ToolBlockStyle.GetMarginRight()
	for i := contentEndCol + 1; i < cardEnd; i++ {
		cell := cells[i]
		if cell.IsZero() || cell.Content != " " {
			continue
		}
		trailingSpaces++
		if !colorsEqual(cell.Style.Bg, toolBg) {
			t.Fatalf("trailing space at col %d background = %v, want tool card bg %v", i, cell.Style.Bg, toolBg)
		}
	}
	if trailingSpaces == 0 {
		t.Fatal("expected trailing padding spaces on wrapped continuation line")
	}
}

func TestToolExpandedResultLinesHiddenCountDoesNotDoubleCountFirstHiddenLine(t *testing.T) {
	lines := make([]string, maxToolCallCompactResultLines+2)
	for i := range lines {
		lines[i] = fmt.Sprintf("line-%02d", i+1)
	}

	visible, hidden := toolExpandedResultLines(strings.Join(lines, "\n"), 100, false)
	if len(visible) != maxToolCallCompactResultLines {
		t.Fatalf("visible lines = %d, want %d", len(visible), maxToolCallCompactResultLines)
	}
	if hidden != 2 {
		t.Fatalf("hidden lines = %d, want 2", hidden)
	}
}

func TestToolExpandedResultLinesCapsCollapsedWrappedDisplayLines(t *testing.T) {
	result := strings.TrimSpace(strings.Repeat("segment ", maxToolCallCompactResultLines+3))

	visible, hidden := toolExpandedResultLines(result, len("segment"), false)
	if len(visible) != maxToolCallCompactResultLines {
		t.Fatalf("collapsed visible lines = %d, want %d: %#v", len(visible), maxToolCallCompactResultLines, visible)
	}
	if hidden != 3 {
		t.Fatalf("collapsed hidden lines = %d, want 3", hidden)
	}

	expanded, hidden := toolExpandedResultLines(result, len("segment"), true)
	if len(expanded) != maxToolCallCompactResultLines+3 {
		t.Fatalf("expanded visible lines = %d, want %d", len(expanded), maxToolCallCompactResultLines+3)
	}
	if hidden != 0 {
		t.Fatalf("expanded hidden lines = %d, want 0", hidden)
	}
}

func TestQueuedToolHeaderShowsQueuedLabelWithoutSpinner(t *testing.T) {
	block := &Block{
		ID:                         1,
		Type:                       BlockToolCall,
		ToolName:                   "shell",
		Content:                    `{"command":"command -v benchstat || true"}`,
		Collapsed:                  true,
		ResultDone:                 false,
		ToolExecutionState:         agent.ToolCallExecutionStateQueued,
		ToolQueuedByExecutionEvent: true,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(96, "●"), "\n"))
	if !strings.Contains(joined, "⏸ shell command -v benchstat || true") {
		t.Fatalf("expected queued Shell header; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Queued") {
		t.Fatalf("expected queued header badge; got:\n%s", joined)
	}
	for _, seg := range activeToolSpinnerSegments {
		if strings.Contains(joined, seg) {
			t.Fatalf("queued tool should not render active spinner segment %q; got:\n%s", seg, joined)
		}
	}
}

// toolOutcomeNonEmptyLineCount is the single implementation behind the
// collapsed-card body decision and the shell status line, so it has to count
// real content across every line terminator a tool result can arrive with.
func TestToolOutcomeNonEmptyLineCount(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"   \n\t\n", 0},
		{"exit code 1", 1},
		{"  exit code 1  \n\n", 1},
		{"file not found\nDid you mean:\n- a.go", 3},
		{"first\r\nsecond\r\n", 2},
		{"first\rsecond", 2},
		{"first\n\n\nsecond", 2},
	}
	for _, c := range cases {
		if got := toolOutcomeNonEmptyLineCount(c.in); got != c.want {
			t.Errorf("toolOutcomeNonEmptyLineCount(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestGenericToolHeaderAndExpandedResultEscapesANSIRichText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "glob",
		Content:       `{"pattern":"\u001b[33m*.go\u001b[0m","path":"\u001b[35m/tmp/repo\u001b[0m"}`,
		Collapsed:     true,
		ResultContent: "\x1b[31minternal/tui/app.go\x1b[0m\ninternal/tui/block.go",
		ResultDone:    true,
	}

	block.ToggleAtWidth(100)

	joined := terminaltext.StripANSI(strings.Join(block.Render(100, ""), "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected generic tool card to not contain raw ESC: %q", joined)
	}
	for _, want := range []string{"2 files", `\x1b[31minternal/tui/app.go\x1b[0m`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected generic tool card to contain %q, got:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "✓ ▾ glob") {
		t.Fatalf("expected expanded glob card to show the expanded disclosure glyph, got:\n%s", joined)
	}
}

func TestActiveToolHeaderKeepsToolNameStableWithoutCallingText(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "shell",
		Content:            `{"command":"echo hi"}`,
		Collapsed:          true,
		ResultDone:         false,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(80, "●"), "\n"))
	if strings.Contains(joined, "calling") {
		t.Fatalf("active tool header should not render calling text; got:\n%s", joined)
	}
	if !strings.Contains(joined, "shell echo hi") {
		t.Fatalf("expected active tool header to keep stable tool name and summary; got:\n%s", joined)
	}
}

func TestQueuedToolHeaderBadgeKeepsRightPadding(t *testing.T) {
	line := renderQueuedToolHeaderBadge("  ▸ Shell", 24)
	plain := terminaltext.StripANSI(line)
	if got := ansi.StringWidth(plain); got != 24 {
		t.Fatalf("queued header width = %d, want 24; got %q", got, plain)
	}
	if !strings.HasSuffix(plain, "Queued ") {
		t.Fatalf("expected queued badge to keep one trailing content-column space; got %q", plain)
	}
}

func TestQueuedToolHeaderBadgeHidesWhenWidthIsTooNarrow(t *testing.T) {
	line := renderQueuedToolHeaderBadge("  ▸ VeryLongToolName", 20)
	plain := terminaltext.StripANSI(line)
	if strings.Contains(plain, "Queued") {
		t.Fatalf("expected queued badge to hide when width is too narrow; got %q", plain)
	}
	if plain != "  ▸ VeryLongToolName" {
		t.Fatalf("queued header = %q, want original trimmed header", plain)
	}
}

func TestPrewrappedToolCardRestoresCardBackgroundAfterTruncatedANSILine(t *testing.T) {
	ApplyTheme(uitheme.Default())
	bgSeq := terminaltext.BackgroundSequence(currentStyles.Palette.ToolCallBg)
	if bgSeq == "" {
		t.Fatal("default tool card background should produce an ANSI background sequence")
	}

	innerANSI := "\x1b[48;5;236m" + strings.Repeat("x", 80)
	lines := renderPrewrappedToolCard(currentStyles.ToolBlockStyle, 24, toolCardTitle("TOOL CALL", 1), []string{innerANSI}, currentStyles.Palette.ToolCallBg, cardRailPrefix("tool", false))
	var body string
	for _, line := range lines {
		if strings.Contains(terminaltext.StripANSI(line), strings.Repeat("x", 24)) {
			body = line
			break
		}
	}
	if body == "" {
		t.Fatalf("rendered truncated body line not found in:\n%q", strings.Join(lines, "\n"))
	}

	paddingAfterTruncatedReset := ansi.ResetStyle + bgSeq + strings.Repeat(" ", currentStyles.ToolBlockStyle.GetPaddingRight()) + ansi.ResetStyle
	if !strings.Contains(body, paddingAfterTruncatedReset) {
		t.Fatalf("truncated ANSI body line did not restore tool card background before right padding; want substring %q in %q", paddingAfterTruncatedReset, body)
	}
}

func TestActiveToolHeaderUsesProvidedSpinnerFrame(t *testing.T) {
	block := &Block{
		ID:                 1,
		Type:               BlockToolCall,
		ToolName:           "write",
		Content:            `{"path":"demo.txt","content":"hello"}`,
		Collapsed:          true,
		ResultDone:         false,
		ToolExecutionState: agent.ToolCallExecutionStateRunning,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(80, "◳"), "\n"))
	if !strings.Contains(joined, "◳") {
		t.Fatalf("expected active tool header to use provided spinner frame; got:\n%s", joined)
	}
}

func TestActiveToolSpinnerSegmentsRotateClockwise(t *testing.T) {
	want := []string{"▖", "▘", "▝", "▗"}
	got := activeToolSpinnerSegments[:]
	if len(got) != len(want) {
		t.Fatalf("spinner segment count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spinner segment %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestToolStatusPrefixesUseSemanticColors(t *testing.T) {
	tests := []struct {
		name   string
		block  Block
		marker string
		ansi   string
	}{
		{
			name: "success",
			block: Block{
				Type:                   BlockToolCall,
				ToolName:               "read",
				Content:                `{"path":"README.md"}`,
				ResultContent:          "ok",
				ResultStatus:           message.ToolStatusSuccess,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			},
			marker: "✓",
			ansi:   "\x1b[38;5;82m✓",
		},
		{
			name: "error",
			block: Block{
				Type:                   BlockToolCall,
				ToolName:               "read",
				Content:                `{"path":"README.md"}`,
				ResultContent:          "Error: denied",
				ResultStatus:           message.ToolStatusError,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			},
			marker: "✗",
			ansi:   "\x1b[38;5;" + currentStyles.Palette.ErrorFg + "m✗",
		},
		{
			name: "cancelled",
			block: Block{
				Type:                   BlockToolCall,
				ToolName:               "read",
				Content:                `{"path":"README.md"}`,
				ResultContent:          "Cancelled",
				ResultStatus:           message.ToolStatusCancelled,
				ResultDone:             true,
				ToolCallDetailExpanded: false,
			},
			marker: "◌",
			ansi:   "\x1b[38;5;250m◌",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			joinedANSI := strings.Join(tt.block.Render(80, ""), "\n")
			joinedPlain := terminaltext.StripANSI(joinedANSI)
			// A card whose body can be folded carries a disclosure marker
			// between the status glyph and the tool name; the color assertion
			// below still pins the status glyph itself.
			prefix := tt.marker + " " + tt.block.ToolName
			if !strings.Contains(joinedPlain, prefix) &&
				!strings.Contains(joinedPlain, tt.marker+" "+toolDisclosureCollapsed+" "+tt.block.ToolName) &&
				!strings.Contains(joinedPlain, tt.marker+" "+toolDisclosureExpanded+" "+tt.block.ToolName) {
				t.Fatalf("expected plain prefix %q (optionally behind a disclosure marker); got:\n%s", prefix, joinedPlain)
			}
			if !strings.Contains(joinedANSI, tt.ansi) {
				t.Fatalf("expected semantic color sequence %q; got:\n%q", tt.ansi, joinedANSI)
			}
		})
	}
}

func TestWebFetchHeaderShowsURLAndTimeout(t *testing.T) {
	const url = "https://example.invalid/documentation-images.html"
	cases := []struct {
		name     string
		args     string
		want     string
		noOption bool
	}{
		{
			name: "non-default deadline renders as a duration",
			args: `{"url":"` + url + `","timeout_ms":45000}`,
			want: "web_fetch " + url + " (timeout=45s)",
		},
		{
			name:     "default deadline stays off the header",
			args:     `{"url":"` + url + `","timeout_ms":30000}`,
			want:     "web_fetch " + url,
			noOption: true,
		},
		{
			name: "over-cap deadline names the effective value",
			args: `{"url":"` + url + `","timeout_ms":300000}`,
			want: "web_fetch " + url + " (timeout=5m→2m)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block := &Block{
				ID:            1,
				Type:          BlockToolCall,
				ToolName:      tools.NameWebFetch,
				Content:       tc.args,
				ResultContent: "URL: " + url + "\nContent-Type: text/html",
				ResultDone:    true,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("expected web_fetch header to contain %q; got:\n%s", tc.want, joined)
			}
			if tc.noOption && strings.Contains(joined, "timeout=") {
				t.Fatalf("default deadline must not appear on the header; got:\n%s", joined)
			}
		})
	}
}

func TestCollapsedGenericToolDeduplicatesMatchingParamAndResultPreview(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "Task",
		Collapsed:              true,
		Content:                `{"command":"[Image #1]","description":"Subdivide dimension-choice failures into purer semantic subtypes"}`,
		ResultContent:          "[Image #1]\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	joined := terminaltext.StripANSI(strings.Join(block.Render(90, ""), "\n"))
	// The first argument is the card's subject, so it rides the header.
	if !strings.Contains(joined, "Task [Image #1]") {
		t.Fatalf("expected the subject on the header, got:\n%s", joined)
	}
	if strings.Count(joined, "[Image #1]") != 1 {
		t.Fatalf("expected duplicated result first line to be suppressed, got:\n%s", joined)
	}
	if !strings.Contains(joined, "✓ ▸ Task") {
		t.Fatalf("expected collapsed generic card to show ▸ disclosure marker in the header, got:\n%s", joined)
	}
}

func TestCollapsedMCPToolUsesHeaderDisclosureMarker(t *testing.T) {
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "mcp_exa_web_search_exa",
		Content:                `{"query":"OpenAI Responses API","numResults":6}`,
		ResultContent:          "title one\nhttps://example.com/one\ntitle two\nhttps://example.com/two\ntitle three\nhttps://example.com/three\ntitle four\nhttps://example.com/four\ntitle five\nhttps://example.com/five\ntitle six\nhttps://example.com/six",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ mcp_exa_web_search_exa") {
		t.Fatalf("expected collapsed MCP card to show header disclosure glyph; got:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "numResults=6") {
		t.Fatalf("expected collapsed MCP card to keep the param summary; got:\n%s", collapsed)
	}
	for _, forbidden := range []string{"more lines", "[space] expand", "[space] collapse"} {
		if strings.Contains(collapsed, forbidden) {
			t.Fatalf("expected collapsed MCP card to drop the footer expand hint (%q); got:\n%s", forbidden, collapsed)
		}
	}

	block.ToggleAtWidth(120)
	expanded := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(expanded, "✓ ▾ mcp_exa_web_search_exa") {
		t.Fatalf("expected expanded MCP card to show the expanded disclosure glyph; got:\n%s", expanded)
	}
	if !strings.Contains(expanded, "https://example.com/six") {
		t.Fatalf("expected expanded MCP card to reveal the full result; got:\n%s", expanded)
	}
}

func TestCollapsedMCPToolWithSingleHiddenLineStaysCollapsed(t *testing.T) {
	// A multiline function arg first plus a scalar second yields exactly one
	// hidden param line, and a short result yields zero hidden result lines.
	// The generic "only one hidden line" heuristic must not pin such an MCP
	// card expanded: external program output stays folded by default and Space
	// must keep working in both directions.
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "mcp_chrome_devtools_evaluate_script",
		Content:                `{"function":"() => {\n  const hits = [];\n  return hits;\n}","pageId":2}`,
		ResultContent:          "Script ran on page and returned:\n```json\n[]\n```",
		ResultDone:             true,
		ToolCallDetailExpanded: false,
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ mcp_chrome_devtools_evaluate_script") {
		t.Fatalf("expected collapsed MCP card to show the collapsed disclosure glyph; got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "const hits") {
		t.Fatalf("expected collapsed MCP card to hide the function body; got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "Script ran on page") {
		t.Fatalf("expected collapsed MCP card to hide the result body; got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(expanded, "✓ ▾ mcp_chrome_devtools_evaluate_script") {
		t.Fatalf("expected expanded MCP card to show the expanded disclosure glyph; got:\n%s", expanded)
	}
	if !strings.Contains(expanded, "const hits") {
		t.Fatalf("expected expanded MCP card to reveal the function body; got:\n%s", expanded)
	}

	block.ToggleAtWidth(120)
	recollapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(recollapsed, "✓ ▸ mcp_chrome_devtools_evaluate_script") {
		t.Fatalf("expected second toggle to restore the collapsed glyph; got:\n%s", recollapsed)
	}
}

func TestGenericToolCardHeaderCanUseWideViewport(t *testing.T) {
	pattern := strings.Repeat("a", maxTextWidth+20)
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "grep",
		Content:    fmt.Sprintf(`{"pattern":%q}`, pattern),
		ResultDone: true,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(maxTextWidth+100, ""), "\n"))
	if !strings.Contains(joined, strings.Repeat("a", 80)) {
		t.Fatalf("expected grep header to use wide viewport beyond text cap; got:\n%s", joined)
	}
}

func TestHandoffHeaderRecoversInvalidPlanPath(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameHandoff,
		Content:       `{"plan_path":42}`,
		ResultDone:    true,
		ResultStatus:  message.ToolStatusError,
		ResultContent: "arguments do not match handoff schema: args.plan_path must be a string, got number 42",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderHandoffCall(140, ""), "\n"))
	if !strings.Contains(plain, "42") {
		t.Fatalf("expected handoff header to show the offending plan_path value, got:\n%s", plain)
	}
}

func TestRenderConfirmFieldEscapesANSIRichLiteralValue(t *testing.T) {
	field := newConfirmLiteralField("Command", "\x1b[31mrm -rf /tmp/demo\x1b[0m", true)
	lines := renderConfirmField(field, 60)
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if strings.ContainsRune(joined, '\x1b') {
		t.Fatalf("expected confirm field to not contain raw ESC: %q", joined)
	}
	if !strings.Contains(joined, `\x1b[31mrm -rf /tmp/demo\x1b[0m`) {
		t.Fatalf("expected confirm field to show ANSI literally, got:\n%s", joined)
	}
}

func TestSanitizeToolDisplayTextEscapesBareCarriageReturnLiterally(t *testing.T) {
	got := sanitizeToolDisplayText("progress 10%\rprogress 90%")
	if containsRawCarriageReturnForTest(got) {
		t.Fatalf("expected sanitized text to not contain raw carriage return: %q", got)
	}
	if !strings.Contains(got, `progress 10%\rprogress 90%`) {
		t.Fatalf("expected bare carriage return to display literally, got %q", got)
	}
}

func TestSanitizeToolDisplayTextPreservesCRLFAsLogicalNewline(t *testing.T) {
	got := sanitizeToolDisplayText("line1\r\nline2")
	if containsRawCarriageReturnForTest(got) {
		t.Fatalf("expected CRLF sanitization to not contain raw carriage return: %q", got)
	}
	if got != "line1\nline2" {
		t.Fatalf("sanitizeToolDisplayText(CRLF) = %q, want %q", got, "line1\nline2")
	}
}

func TestCollapsedSummaryEscapesBareCarriageReturnLiterally(t *testing.T) {
	var lines []string
	appendCollapsedSummaryLines(&lines, "phase 1\rphase 2", 80, currentStyles.ToolResultStyle)
	joined := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if containsRawCarriageReturnForTest(joined) {
		t.Fatalf("expected collapsed summary to not contain raw carriage return: %q", joined)
	}
	if !strings.Contains(joined, `phase 1\rphase 2`) {
		t.Fatalf("expected collapsed summary to show bare carriage return literally, got:\n%s", joined)
	}
}

func TestToolResultTableLikeContentUsesPlainText(t *testing.T) {
	ApplyTheme(uitheme.Default())
	block := &Block{
		ID:                     1,
		Type:                   BlockToolCall,
		ToolName:               "web_fetch",
		Content:                `{"url":"https://example.com"}`,
		ResultDone:             true,
		ResultStatus:           message.ToolStatusSuccess,
		ToolCallDetailExpanded: true,
		ResultContent:          "The report shows 1 expired account email entry:\n\n| Account ID | Email | Expiration Date |\n|------------|-------|-----------------|\n| a20158b8-... | gfwgfwgfwgfw@gmail.com | 2026-04-02 |\n\nThese accounts need to sign in again.",
	}

	joinedPlain := terminaltext.StripANSI(stripOSC8ToolTest(strings.Join(block.Render(100, ""), "\n")))
	for _, want := range []string{"| Account ID | Email | Expiration Date |", "gfwgfwgfwgfw@gmail.com", "These accounts need to sign in again."} {
		if !strings.Contains(joinedPlain, want) {
			t.Fatalf("expected tool result to keep table-like content literal (%q), got %q", want, joinedPlain)
		}
	}
}

func TestRenderedCodeTokensKeepContrastOnCurrentTheme(t *testing.T) {
	theme := uitheme.Default()
	sample := "package main; func sample() { /* comment */ println(123, \"text\") }"
	for _, bg := range []string{theme.ToolCallBg, theme.DiffAddLineBg, theme.DiffDelLineBg} {
		rendered := syntax.New("sample.go", sample, "").Line(sample, bg)
		for _, cell := range renderedCellsForText(t, rendered, sample) {
			if cell.Style.Fg == (uv.Color{}) {
				continue
			}
			if got := contrastRatio(cell.Style.Fg, lipgloss.Color(bg)); got < 4.5 {
				t.Fatalf("rendered token %q contrast on %s = %.2f", cell.Content, bg, got)
			}
		}
	}
}
