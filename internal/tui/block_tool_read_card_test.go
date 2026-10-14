package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
	"github.com/keakon/chord/internal/tui/syntax"
	"github.com/keakon/chord/internal/tui/terminaltext"
	uitheme "github.com/keakon/chord/internal/tui/theme"
)

func TestReadOffsetTOMLFragmentKeepsErrorTokensReadable(t *testing.T) {
	ApplyTheme(uitheme.Default())
	content := "以下职责只属于主控，不得委托 Agent 自行执行"
	args, _ := json.Marshal(map[string]any{"path": ".codex/config.toml", "offset": 15, "limit": 1})
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameRead,
		Content:       string(args),
		ResultContent: content,
		ResultDone:    true,
	}

	line := renderedLineContaining(t, block.Render(100, ""), content)
	assertRenderedTextForeground(t, line, "职", renderedCellsForText(t, syntax.New("sample.toml", "", "").Line(content, ""), "职")[0].Style.Fg)
	assertRenderedTextBackground(t, line, "职", colorOfTheme(currentStyles.Palette.ToolCallBg))
}

func TestReadHeaderShowsRelativePathInsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(wd, "internal", "tui", "block_tool.go")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "read",
		Content:           fmt.Sprintf(`{"path":%q,"limit":20,"offset":5}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	want := filepath.Join("internal", "tui", "block_tool.go") + " (offset=5, limit=20)"
	if !strings.Contains(joined, want) {
		t.Fatalf("expected Read header to show relative path; got:\n%s", joined)
	}
	if strings.Contains(joined, abs) {
		t.Fatalf("did not expect Read header to show absolute path; got:\n%s", joined)
	}
}

func TestReadCardStripsIgnoredArgNoteFromFilePreview(t *testing.T) {
	// The read preview repaints every result line as file content, so a runtime
	// note appended after the payload used to read as an extra file line.
	note := "Note: ignored unrecognized parameter(s): args.line_limit"
	block := &Block{
		ID:                     3,
		Type:                   BlockToolCall,
		ToolName:               tools.NameRead,
		Content:                `{"path":"internal/tui/block.go","line_limit":10}`,
		ResultContent:          "READ_RESULT lines=1-1 total=1\npackage tui\n" + note,
		ResultNotes:            []string{note},
		ResultDone:             true,
		ToolCallDetailExpanded: true,
		Audit: &message.ToolArgsAudit{
			OriginalArgsJSON:  `{"path":"internal/tui/block.go","line_limit":10}`,
			EffectiveArgsJSON: `{"path":"internal/tui/block.go"}`,
			IgnoredArgs: []message.IgnoredToolArg{{
				Path:      "args.line_limit",
				ValueJSON: "10",
				Reason:    message.IgnoredToolArgReasonUnrecognized,
			}},
		},
	}
	rendered := terminaltext.StripANSI(strings.Join(block.Render(160, ""), "\n"))
	if !strings.Contains(rendered, "ignored line_limit=10") {
		t.Fatalf("expected the ignored argument on the header; got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "package tui") {
		t.Fatalf("expected the file preview in the body; got:\n%s", rendered)
	}
	if strings.Contains(rendered, "Note:") {
		t.Fatalf("did not expect the runtime note in the file preview; got:\n%s", rendered)
	}
	if copied := blockCopyContent(block); !strings.Contains(copied, note) {
		t.Fatalf("expected the copied card to retain the model-facing note; got:\n%s", copied)
	}
}

func TestReadCollapsedShowsSummaryAndExpandedShowsAllReturnedLines(t *testing.T) {
	ApplyTheme(uitheme.Default())
	lines := make([]string, 0, 260)
	for i := 1; i <= 260; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      tools.NameRead,
		Content:       `{"path":"sample.go","limit":260}`,
		Collapsed:     true,
		ResultDone:    true,
		ResultContent: "READ_RESULT lines=1-260 total=343\n" + strings.Join(lines, "\n"),
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(collapsed, "✓ ▸ read sample.go (limit=260) · lines 1–260 of 343") {
		t.Fatalf("expected collapsed read summary, got:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "line 1") {
		t.Fatalf("collapsed read should hide body, got:\n%s", collapsed)
	}

	block.ToggleAtWidth(120)
	expanded := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	for _, want := range []string{"✓ ▾ read", "1  line 1", "260  line 260"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expected expanded read to contain %q, got:\n%s", want, expanded)
		}
	}
}

func TestReadCardDoesNotTreatSourceAsArtifactMetadata(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   tools.NameRead,
		Content:    `{"path":"internal/tui/block_tool_common.go","offset":620,"limit":160}`,
		Collapsed:  true,
		ResultDone: true,
		ResultContent: "READ_RESULT lines=621-622 total=982 truncated=budget requested_lines=621-780\n" +
			`if idx := strings.LastIndex(trimmed, "Full output saved to "); idx >= 0 {` + "\n" +
			`rest := strings.TrimSpace(trimmed[idx+len("Full output saved to "):])` + "\n\n" +
			"Full output saved to /session/tool-outputs/read-result.log. Only if the preview is insufficient, use grep first or read with offset/limit for needed ranges; use a script/parser for huge single-line structured output. Do not read the entire output by default.",
	}

	collapsed := terminaltext.StripANSI(strings.Join(block.Render(320, ""), "\n"))
	if !strings.Contains(collapsed, "lines 621–622 of 982") {
		t.Fatalf("expected collapsed read summary to keep the returned line range, got:\n%s", collapsed)
	}
	for _, hidden := range []string{"output truncated", "stale result", "superseded result", "full output saved to", `"):]`} {
		if strings.Contains(collapsed, hidden) {
			t.Fatalf("collapsed read summary should not contain %q, got:\n%s", hidden, collapsed)
		}
	}

	block.ToggleAtWidth(320)
	expandedLines := stripANSILines(block.Render(320, ""))
	expanded := strings.Join(expandedLines, "\n")
	if !strings.Contains(expanded, "output truncated") {
		t.Fatalf("expanded read result should preserve the truncation state, got:\n%s", expanded)
	}
	header := ""
	for _, line := range expandedLines {
		if strings.Contains(line, "▾ read ") {
			header = line
			break
		}
	}
	if header == "" {
		t.Fatalf("expanded read card is missing its header:\n%s", strings.Join(expandedLines, "\n"))
	}
	if strings.Contains(strings.ToLower(header), "full output saved to") || strings.Contains(header, `"):]`) {
		t.Fatalf("expanded read header should not contain source text, got %q", header)
	}
	for _, want := range []string{`621  if idx := strings.LastIndex(trimmed, "Full output saved to "); idx >= 0 {`, `622  rest := strings.TrimSpace(trimmed[idx+len("Full output saved to "):])`} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded read result should preserve source line %q, got:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "Full output saved to /session/tool-outputs/read-result.log.") {
		t.Fatalf("expanded read result should hide the historical artifact footer, got:\n%s", expanded)
	}
}

func TestReadHeaderKeepsAbsolutePathOutsideWorkingDir(t *testing.T) {
	wd := filepath.Join(string(os.PathSeparator), "tmp", "workspace")
	abs := filepath.Join(string(os.PathSeparator), "tmp", "other", "demo.txt")
	block := &Block{
		ID:                1,
		Type:              BlockToolCall,
		ToolName:          "read",
		Content:           fmt.Sprintf(`{"path":%q}`, abs),
		ResultDone:        true,
		displayWorkingDir: wd,
	}
	joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
	if !strings.Contains(joined, abs) {
		t.Fatalf("expected Read header to keep absolute path outside working dir; got:\n%s", joined)
	}
}

func TestReadCollapsedSummaryDistinguishesTruncationKinds(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		wantLabel  string
		notWantAbs string
	}{
		{"budget", "READ_RESULT lines=1-487 total=2000 truncated=budget requested_lines=1-900", "lines 1–487 of 2000", "output truncated"},
		{"stale", "READ_RESULT lines=41-103 total=200 truncated=stale", "lines 41–103 of 200", "stale result"},
		{"superseded", "READ_RESULT lines=10-12 total=80 truncated=superseded", "lines 10–12 of 80", "superseded result"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := &Block{
				ID:            1,
				Type:          BlockToolCall,
				ToolName:      "read",
				Content:       `{"path":"internal/tui/example.go"}`,
				ResultContent: tt.header + "\nline one\nline two",
				ResultDone:    true,
				Collapsed:     true,
			}

			joined := terminaltext.StripANSI(strings.Join(block.Render(120, ""), "\n"))
			if !strings.Contains(joined, tt.wantLabel) {
				t.Fatalf("expected collapsed read summary to show %q, got:\n%s", tt.wantLabel, joined)
			}
			if tt.notWantAbs != "" && strings.Contains(joined, tt.notWantAbs) {
				t.Fatalf("expected collapsed read summary not to show %q, got:\n%s", tt.notWantAbs, joined)
			}
			if !strings.Contains(joined, "✓ ▸ read") {
				t.Fatalf("expected collapsed read card to show the collapsed disclosure glyph, got:\n%s", joined)
			}
		})
	}
}

func TestCancelledReadCallOmitsDuplicateCancelledText(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"path":"foo.txt","limit":10}`,
		ResultContent: "Cancelled",
		ResultStatus:  message.ToolStatusCancelled,
		ResultDone:    true,
	}

	plain := terminaltext.StripANSI(strings.Join(block.Render(80, ""), "\n"))
	if !strings.Contains(plain, "read foo.txt") {
		t.Fatalf("expected Read header to include file path, got:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Cancelled") {
		t.Fatalf("expected cancelled summary line, got:\n%s", plain)
	}
	if strings.Contains(plain, "↳ Cancelled:") {
		t.Fatalf("expected Read cancelled card to omit duplicate cancelled detail header, got:\n%s", plain)
	}
	if strings.Count(plain, "Cancelled") != 1 {
		t.Fatalf("expected exactly one visible Cancelled marker, got:\n%s", plain)
	}
}

func TestReadCallRendersSingleBlankLineWithoutPanic(t *testing.T) {
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"path":"internal/tui/input.go","limit":1,"offset":358}`,
		ResultDone:    true,
		ResultContent: "\n",
	}

	lines := block.renderReadCall(80, "")
	plain := terminaltext.StripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "read internal/tui/input.go") {
		t.Fatalf("expected Read header to remain visible, got:\n%s", plain)
	}
	// offset is a 1-based start line, so the single blank line is line 358.
	if !strings.Contains(plain, "358") {
		t.Fatalf("expected blank numbered line to render safely, got:\n%s", plain)
	}
	if strings.Contains(plain, "panic") {
		t.Fatalf("unexpected panic text in rendered output: %s", plain)
	}
}

// TestReadCallDisclosureMatchesToggleableBody pins that the read card's ▸/▾
// marker and its toggle agree: a card with a body the toggle reveals shows a
// marker, and a card with nothing to reveal neither shows one nor flips state.
func TestReadCallDisclosureMatchesToggleableBody(t *testing.T) {
	tests := []struct {
		name           string
		block          *Block
		wantMarker     bool
		wantToggleable bool
	}{
		{
			name: "legacy success with body",
			block: &Block{
				ID: 1, Type: BlockToolCall, ToolName: tools.NameRead,
				Content: `{"path":"a.go"}`, ResultDone: true, Collapsed: true,
				ResultContent: "package main\n",
			},
			wantMarker: true, wantToggleable: true,
		},
		{
			name: "success without body",
			block: &Block{
				ID: 2, Type: BlockToolCall, ToolName: tools.NameRead,
				Content: `{"path":"a.go"}`, ResultDone: true, Collapsed: true,
			},
			wantMarker: false, wantToggleable: false,
		},
		{
			name: "single-line error",
			block: &Block{
				ID: 3, Type: BlockToolCall, ToolName: tools.NameRead,
				Content: `{"path":"a.go"}`, ResultDone: true, Collapsed: true,
				ResultStatus: message.ToolStatusError, ResultContent: "Error: denied",
			},
			wantMarker: false, wantToggleable: false,
		},
		{
			name: "multi-line error already fully shown",
			block: &Block{
				ID: 4, Type: BlockToolCall, ToolName: tools.NameRead,
				Content: `{"path":"a.go"}`, ResultDone: true, Collapsed: true,
				ResultStatus: message.ToolStatusError, ResultContent: "Error: first\nsecond",
			},
			wantMarker: false, wantToggleable: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plain := terminaltext.StripANSI(strings.Join(tt.block.Render(80, ""), "\n"))
			hasMarker := strings.Contains(plain, toolDisclosureCollapsed) || strings.Contains(plain, toolDisclosureExpanded)
			if hasMarker != tt.wantMarker {
				t.Fatalf("marker present = %v, want %v; render:\n%s", hasMarker, tt.wantMarker, plain)
			}
			if got := tt.block.ToggleAtWidth(80); got != tt.wantToggleable {
				t.Fatalf("ToggleAtWidth = %v, want %v", got, tt.wantToggleable)
			}
		})
	}
}

func TestReadCallWideHeaderPreservesOffsetWithLongPath(t *testing.T) {
	longPath := "/fictional/workspace/generated/reports/very/deeply/nested/directories/synthetic-review-fixture-00000000000000000001.txt"
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       fmt.Sprintf(`{"path":%q,"limit":160,"offset":430}`, longPath),
		ResultDone:    true,
		ResultContent: "line 431\nline 432",
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderReadCall(180, ""), "\n"))
	if !strings.Contains(plain, "(offset=430, limit=160)") {
		t.Fatalf("expected read header to preserve full offset/limit params, got:\n%s", plain)
	}
}

func TestReadHeaderUsesEllipsisWhenPathDoesNotFit(t *testing.T) {
	longPath := "/fictional/workspace/generated/reports/very/deeply/nested/directories/synthetic-review-fixture-00000000000000000001.txt"
	header := terminaltext.StripANSI(renderReadHeaderLine("✓", "read", longPath, "offset=430, limit=160", "", 80))

	if !strings.Contains(header, "…") {
		t.Fatalf("expected read header to use ellipsis for long path, got %q", header)
	}
	if !strings.Contains(header, "(offset=430, limit=160)") {
		t.Fatalf("expected read header to preserve params while truncating path, got %q", header)
	}
}

func TestReadCallEscapesANSIRichDumpContent(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"/tmp/chord-diag/tui-dump.txt","limit":120,"offset":1535}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"\x1b[38;5;61m│\x1b[m\x1b[48;5;235m dump line\x1b[m",
			"[screen_buffer]",
		}, "\n"),
	}

	rows, source := parseReadDisplayLines(block.ResultContent, 1536)
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	if strings.ContainsRune(rows[0].Content, '\x1b') {
		t.Fatalf("sanitized Read row should not contain raw ESC: %q", rows[0].Content)
	}
	if strings.ContainsRune(source, '\x1b') {
		t.Fatalf("sanitized Read source sample should not contain raw ESC: %q", source)
	}
	if !strings.Contains(rows[0].Content, `\x1b[38;5;61m│\x1b[m\x1b[48;5;235m dump line\x1b[m`) {
		t.Fatalf("sanitized Read row should expose ANSI literally, got %q", rows[0].Content)
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderReadCall(140, ""), "\n"))
	for _, want := range []string{
		`read /tmp/chord-diag/tui-dump.txt`,
		`\x1b[38;5;61m│\x1b[m\x1b[48;5;235m dump line\x1b[m`,
		`[screen_buffer]`,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered read card to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestReadCallStripsTrailingCarriageReturnsFromPersistedOutput(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.csv","limit":20}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"issue,label\r",
			"\"a\",\"b\"\r",
			"(showing lines 1-2 of 10)\r",
		}, "\n"),
	}

	rows, source := parseReadDisplayLines(block.ResultContent, 1)
	if len(rows) != 3 {
		t.Fatalf("rows len = %d, want 3", len(rows))
	}
	for i, row := range rows {
		if containsRawCarriageReturnForTest(row.Content) {
			t.Fatalf("row %d content should not contain raw carriage return: %q", i, row.Content)
		}
	}
	if containsRawCarriageReturnForTest(source) {
		t.Fatalf("source sample should not contain raw carriage return: %q", source)
	}

	plain := terminaltext.StripANSI(strings.Join(block.renderReadCall(80, ""), "\n"))
	if containsRawCarriageReturnForTest(plain) {
		t.Fatalf("rendered read card should not contain raw carriage return: %q", plain)
	}
	for _, want := range []string{"issue,label", "\"a\",\"b\"", "(showing lines 1-2 of 10)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected rendered read card to contain %q, got:\n%s", want, plain)
		}
	}
}

func TestReadCallTreatsReadResultHeaderAsMetadata(t *testing.T) {
	result := strings.Join([]string{
		`READ_RESULT lines=41-42 total=200`,
		"issue,label",
		`"a","b"`,
	}, "\n")

	rows, source := parseReadDisplayLines(result, 1)
	// The READ_RESULT line is model-only metadata; it must not be rendered, and
	// its start line drives code numbering even when the caller passes a stale
	// startLine (here 1).
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2 (header hidden)", len(rows))
	}
	if !rows[0].IsCode || rows[0].LineNo != "41" || rows[0].Content != "issue,label" {
		t.Fatalf("first source row = %#v, want line 41 issue,label", rows[0])
	}
	if !rows[1].IsCode || rows[1].LineNo != "42" || rows[1].Content != `"a","b"` {
		t.Fatalf("second source row = %#v, want line 42 quoted row", rows[1])
	}
	if source != "issue,label\n\"a\",\"b\"" {
		t.Fatalf("source = %q, want raw content without header", source)
	}
}

func TestReadCallHidesEmptyRangeReadResultHeader(t *testing.T) {
	// lines=none is the empty-range header form; it must still be recognized as
	// metadata and hidden, not rendered as a source line.
	rows, source := parseReadDisplayLines("READ_RESULT lines=none total=0", 1)
	if len(rows) != 0 {
		t.Fatalf("rows = %#v, want no rows for empty-range header", rows)
	}
	if source != "" {
		t.Fatalf("source = %q, want empty source", source)
	}
}

func TestReadCallAlignsLineNumberGutterAcrossDigitWidths(t *testing.T) {
	block := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.go","limit":3}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"package main",
			"import \"fmt\"",
			"func main() {}",
		}, "\n"),
	}

	plainLines := strings.Split(terminaltext.StripANSI(strings.Join(block.renderReadCall(100, ""), "\n")), "\n")
	var cols []int
	for _, line := range plainLines {
		for _, marker := range []string{"package main", "import \"fmt\"", "func main() {}"} {
			if idx := strings.Index(line, marker); idx >= 0 {
				cols = append(cols, idx)
			}
		}
	}
	if len(cols) != 3 {
		t.Fatalf("expected three rendered source rows, got columns %v in:\n%s", cols, strings.Join(plainLines, "\n"))
	}
	if cols[0] != cols[1] || cols[1] != cols[2] {
		t.Fatalf("source columns should align across line-number digit widths, got %v in:\n%s", cols, strings.Join(plainLines, "\n"))
	}
}

func TestReadCallUsesCompactLineNumberGutterForSameDigitWidth(t *testing.T) {
	twoDigitBlock := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.go","limit":2,"offset":9}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"alpha",
			"beta",
		}, "\n"),
	}
	// offset 99 yields lines 99-100: one row past the two-digit boundary, so
	// the gutter reserves three digits while the 9..10 case reserves two.
	threeDigitBlock := &Block{
		ID:         1,
		Type:       BlockToolCall,
		ToolName:   "read",
		Content:    `{"path":"sample.go","limit":2,"offset":99}`,
		ResultDone: true,
		ResultContent: strings.Join([]string{
			"alpha",
			"beta",
		}, "\n"),
	}

	twoDigitLines := strings.Split(terminaltext.StripANSI(strings.Join(twoDigitBlock.renderReadCall(100, ""), "\n")), "\n")
	threeDigitLines := strings.Split(terminaltext.StripANSI(strings.Join(threeDigitBlock.renderReadCall(100, ""), "\n")), "\n")
	twoDigitCol := renderedMarkerColumn(twoDigitLines, "alpha")
	threeDigitCol := renderedMarkerColumn(threeDigitLines, "alpha")
	if twoDigitCol < 0 || threeDigitCol < 0 {
		t.Fatalf("expected rendered source rows, got two-digit=%d three-digit=%d\ntwo-digit:\n%s\nthree-digit:\n%s", twoDigitCol, threeDigitCol, strings.Join(twoDigitLines, "\n"), strings.Join(threeDigitLines, "\n"))
	}
	if got, want := threeDigitCol-twoDigitCol, 1; got != want {
		t.Fatalf("two-digit gutter should not reserve a third digit, column delta=%d want %d\ntwo-digit:\n%s\nthree-digit:\n%s", got, want, strings.Join(twoDigitLines, "\n"), strings.Join(threeDigitLines, "\n"))
	}
}

func TestReadCallLineNumberGutterIgnoresRowsBeyondRenderLimit(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	block := &Block{
		ID:            1,
		Type:          BlockToolCall,
		ToolName:      "read",
		Content:       `{"path":"sample.go"}`,
		ResultDone:    true,
		Collapsed:     false,
		ResultContent: strings.Join(lines, "\n"),
	}

	plainLines := strings.Split(terminaltext.StripANSI(strings.Join(block.renderReadCall(100, ""), "\n")), "\n")
	line1Col := renderedMarkerColumn(plainLines, "line 1")
	line200Col := renderedMarkerColumn(plainLines, "line 200")
	if line1Col < 0 || line200Col < 0 {
		t.Fatalf("expected visible rows 1 and 200, got columns %d/%d in:\n%s", line1Col, line200Col, strings.Join(plainLines, "\n"))
	}
	if line1Col != line200Col {
		t.Fatalf("visible rows should align, got line 1 column %d and line 200 column %d in:\n%s", line1Col, line200Col, strings.Join(plainLines, "\n"))
	}
	if line1000Col := renderedMarkerColumn(plainLines, "line 1000"); line1000Col < 0 {
		t.Fatalf("line 1000 should render in expanded full view, got:\n%s", strings.Join(plainLines, "\n"))
	}
	if got, want := line1Col-renderedMarkerColumn(plainLines, "1  line 1"), len("1  "); got != want {
		t.Fatalf("gutter should stay aligned in expanded full view; got separator offset %d want %d in:\n%s", got, want, strings.Join(plainLines, "\n"))
	}
}

func renderedMarkerColumn(lines []string, marker string) int {
	for _, line := range lines {
		if idx := strings.Index(line, marker); idx >= 0 {
			return idx
		}
	}
	return -1
}
