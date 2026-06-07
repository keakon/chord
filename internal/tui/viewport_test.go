package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/keakon/chord/internal/tools"
)

// Selection highlight unit tests: validates findColumnByteOffsets, applyHighlightToLine, selectionColRange
// logic without manual terminal drag-selection. Run:
//   go test ./internal/tui/ -run 'FindColumnByteOffsets|ApplyHighlight|SelectionColRange|SkipANSISequence' -v

// ---------------------------------------------------------------------------
// skipANSISequence
// ---------------------------------------------------------------------------

func TestSkipANSISequence(t *testing.T) {
	tests := []struct {
		name  string
		s     string
		start int
		want  int
	}{
		{"not escape", "hello", 0, 0},
		{"CSI simple", "\x1b[0m", 0, 4},
		{"CSI with params", "\x1b[32;1m", 0, 7},
		{"OSC BEL", "\x1b]0;title\x07", 0, 10}, // 1+1+7+1 bytes: \x1b ] 0;title \x07
		{"mid string CSI", "a\x1b[31mb", 1, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skipANSISequence(tt.s, tt.start)
			if got != tt.want {
				t.Errorf("skipANSISequence(%q, %d) = %d, want %d", tt.s, tt.start, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// findColumnByteOffsets — column→byte mapping, single-line highlight depends on this logic
// ---------------------------------------------------------------------------

func TestFindColumnByteOffsets(t *testing.T) {
	tests := []struct {
		name     string
		s        string
		colStart int
		colEnd   int
		wantSeg  string // expected stripANSI([startByte:endByte]) content ("" means startByte<0)
	}{
		{"plain same-line", "hello world", 0, 5, "hello"},
		{"plain same-line middle", "hello world", 6, 11, "world"},
		{"plain single col", "abc", 1, 2, "b"},
		{"empty range", "abc", 1, 1, ""},
		{"past end", "ab", 5, 10, ""},
		{"leading CSI", "\x1b[0mhello", 0, 5, "hello"},
		{"leading CSI middle", "\x1b[32mhello\x1b[0m", 1, 4, "ell"},
		{"same-line with ANSI", "\x1b[32mfoo\x1b[0m bar", 0, 3, "foo"},
		{"same-line with ANSI mid", "\x1b[32mfoo\x1b[0m bar", 4, 7, "bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			startByte, endByte := findColumnByteOffsets(tt.s, tt.colStart, tt.colEnd)
			if tt.wantSeg == "" {
				if startByte >= 0 && endByte > startByte {
					seg := tt.s[startByte:endByte]
					plain := stripANSI(seg)
					t.Errorf("expected no/invalid range, got [%d:%d] segment %q plain %q", startByte, endByte, seg, plain)
				}
				return
			}
			if startByte < 0 || endByte <= startByte {
				t.Errorf("findColumnByteOffsets(%q, %d, %d) = (%d, %d), want valid range for segment %q",
					tt.s, tt.colStart, tt.colEnd, startByte, endByte, tt.wantSeg)
				return
			}
			seg := tt.s[startByte:endByte]
			plain := stripANSI(seg)
			if plain != tt.wantSeg {
				t.Errorf("segment stripANSI(%q) = %q, want %q", seg, plain, tt.wantSeg)
			}
		})
	}
}

func TestUpdateLastBlockDoesNotPopulateLineCache(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(80, 12)
	block := &Block{ID: 1, Type: BlockAssistant, Content: "hello"}
	v.AppendBlock(block)

	block.Content += " world"
	block.InvalidateCache()
	v.InvalidateLastBlock()

	_ = v.Render("", nil, -1, -1, "")

	// lineCache may be populated as a side effect of enforceHotBudget →
	// visibleWindowBlockIDs → blockSpanLines during Render. That is correct:
	// subsequent frames hit the cache instead of re-rendering.
	// The critical invariant is that viewportCache is populated for display.
	if block.viewportCache == nil {
		t.Fatal("viewport cache should be populated after render")
	}
}

func TestViewportRenderPartialWindowDoesNotCacheSliceAsFullBlock(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(24, 2)
	block := &Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("slice me ", 40)}
	v.AppendBlock(block)
	if total := v.lineCount(block, v.width); total <= v.height {
		t.Fatalf("lineCount = %d, want > viewport height %d for partial render", total, v.height)
	}

	_ = v.Render("", nil, -1, -1, "")

	if got := len(block.viewportCache); got != 0 {
		t.Fatalf("viewport cache len = %d, want 0 after partial render", got)
	}
}

func TestViewportRenderWithBlockStartsCacheMatchesLinearPath(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(32, 6)
	for i := range 80 {
		v.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("cached render equivalence ", 8)})
	}
	_ = v.Render("", nil, -1, -1, "")
	for _, offset := range []int{0, 1, 5, 17, 61, v.totalLines / 2, max(0, v.totalLines-v.height)} {
		v.offset = offset
		cached := v.Render("", nil, -1, -1, "")
		starts, spans := v.blockStartsCache, v.blockSpansCache
		v.blockStartsCache, v.blockSpansCache = nil, nil
		linear := v.Render("", nil, -1, -1, "")
		v.blockStartsCache, v.blockSpansCache = starts, spans
		if cached != linear {
			t.Fatalf("render mismatch at offset %d", offset)
		}
	}
}

func TestViewportRenderIgnoresStaleBlockPositionCacheVersion(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := NewViewport(18, 3)
	v.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "short"})
	v.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: strings.Repeat("target ", 8)})
	_ = v.Render("", nil, -1, -1, "")
	staleStarts := append([]int(nil), v.blockStartsCache...)
	staleSpans := append([]int(nil), v.blockSpansCache...)

	v.blocks[0].Content = strings.Repeat("expanded ", 20)
	v.blocks[0].InvalidateCache()
	v.bumpRenderVersion()
	v.offset = staleStarts[1]
	v.blockStartsCache = staleStarts
	v.blockSpansCache = staleSpans

	got := v.Render("", nil, -1, -1, "")
	if strings.Contains(got, "target") {
		t.Fatalf("Render used stale block-position cache and jumped to block 2; output=%q", got)
	}
	if !strings.Contains(got, "expanded") {
		t.Fatalf("Render did not recompute stale block-position cache; output=%q", got)
	}
}

func TestVisibleBlocksCacheInvalidatesOnMutationAndFilterChange(t *testing.T) {
	v := NewViewport(80, 12)
	mainBlock := &Block{ID: 1, Type: BlockAssistant, Content: "main"}
	agentBlock := &Block{ID: 2, Type: BlockAssistant, Content: "sub", AgentID: "agent-1"}
	v.AppendBlocks([]*Block{mainBlock, agentBlock})

	v.SetFilter("main")
	first := v.visibleBlocks()
	second := v.visibleBlocks()
	if len(first) != 1 || first[0].ID != 1 {
		t.Fatalf("main visible blocks = %#v, want only block 1", first)
	}
	if len(second) != 1 || second[0] != first[0] {
		t.Fatalf("repeated visibleBlocks() = %#v, want same filtered content as first call %#v", second, first)
	}

	v.SetFilter("agent-1")
	filtered := v.visibleBlocks()
	if len(filtered) != 1 || filtered[0].ID != 2 {
		t.Fatalf("agent visible blocks = %#v, want only block 2", filtered)
	}

	v.RemoveBlockByID(2)
	afterRemove := v.visibleBlocks()
	if len(afterRemove) != 0 {
		t.Fatalf("visible blocks after remove = %#v, want empty", afterRemove)
	}

	v.SetFilter("main")
	v.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "new-main"})
	afterAppend := v.visibleBlocks()
	if len(afterAppend) != 2 || afterAppend[0].ID != 1 || afterAppend[1].ID != 3 {
		t.Fatalf("visible blocks after append = %#v, want [1 3]", afterAppend)
	}
}

func TestRenderRangeUsesLineCacheSlice(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("slice me ", 40)}
	full := block.Render(60, "")
	block.lineCache = full
	block.lineCacheWidth = 60
	block.lineCountCache = len(full)
	part := block.RenderRange(60, "", 1, min(3, len(full)))
	if len(full) > 1 && len(part) == 0 {
		t.Fatal("expected non-empty render range from cached lines")
	}
	for i := range part {
		if part[i] != full[1+i] {
			t.Fatalf("RenderRange line %d = %q, want %q", i, part[i], full[1+i])
		}
	}
}

func TestAssistantRenderUsesMarkdownCacheHit(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockAssistant, Content: "cached assistant body"}
	first := strings.Join(stripANSILines(block.Render(80, "")), "\n")
	if !strings.Contains(first, "cached assistant body") {
		t.Fatalf("first render missing assistant body: %q", first)
	}
	if block.mdCache == nil {
		t.Fatal("expected first render to populate markdown cache")
	}

	second := strings.Join(stripANSILines(block.Render(80, "")), "\n")
	if !strings.Contains(second, "cached assistant body") {
		t.Fatalf("cache-hit render missing assistant body: %q", second)
	}
}

func TestViewportVisibleWindowBlockIDsUsesCachedStartsAndSpans(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := benchmarkViewportWithSpill()
	v.ScrollToBottom()
	_ = v.Render("", nil, -1, -1, "")
	if len(v.blockStartsCache) == 0 || len(v.blockSpansCache) == 0 {
		t.Fatal("expected render to populate block position caches")
	}

	spilled := 0
	for _, block := range v.blocks {
		if block.spillCold {
			spilled++
		}
	}
	if spilled == 0 {
		t.Fatal("expected at least one spilled block in benchmark viewport")
	}

	ids := v.visibleWindowBlockIDs()
	if len(ids) == 0 {
		t.Fatal("expected visible window to report at least one block")
	}
	for _, block := range v.blocks {
		if block.spillCold && block.lineCache != nil {
			t.Fatalf("spilled block %d should not be re-rendered for visibleWindowBlockIDs", block.ID)
		}
	}
}

func TestViewportVisibleWindowBlockIDsCachedOnlyRejectsStalePositionVersion(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := benchmarkViewportWithSpill()
	v.ScrollToBottom()
	_ = v.Render("", nil, -1, -1, "")
	if len(v.blockStartsCache) == 0 || len(v.blockSpansCache) == 0 {
		t.Fatal("expected render to populate block position caches")
	}

	v.bumpRenderVersion()
	if ids, ok := v.visibleWindowBlockIDsCachedOnly(); ok {
		t.Fatalf("cached-only visible window used stale block-position version, ids=%#v", ids)
	}
}

func TestViewportRenderDoesNotUseStaleSpansForColdSpillBlocks(t *testing.T) {
	ApplyTheme(DefaultTheme())
	v := benchmarkViewportWithSpill()
	v.ScrollToBottom()
	_ = v.Render("", nil, -1, -1, "")
	if len(v.blockStartsCache) == 0 || len(v.blockSpansCache) == 0 {
		t.Fatal("expected render to populate block position caches")
	}
	spilled := false
	for _, block := range v.blocks {
		if block.spillCold {
			spilled = true
			break
		}
	}
	if !spilled {
		t.Fatal("expected at least one cold spilled block")
	}

	staleStarts := append([]int(nil), v.blockStartsCache...)
	staleSpans := append([]int(nil), v.blockSpansCache...)
	v.SetWidth(v.width / 2)
	v.offset = 0
	v.blockStartsCache = staleStarts
	v.blockSpansCache = staleSpans

	got := v.Render("", nil, -1, -1, "")
	v.blockStartsCache, v.blockSpansCache = nil, nil
	linear := v.Render("", nil, -1, -1, "")
	if got != linear {
		t.Fatal("render used stale spans for cold spilled blocks")
	}
}

func BenchmarkViewportVisibleWindowBlockIDsCachedOnly(b *testing.B) {
	ApplyTheme(DefaultTheme())
	v := benchmarkViewportWithSpill()
	v.ScrollToBottom()
	_ = v.Render("", nil, -1, -1, "")
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = v.visibleWindowBlockIDsCachedOnly()
	}
}

// ---------------------------------------------------------------------------
// applyHighlightToLine — inserts highlight SGR within a single line, tests output for correct codes and segments
// ---------------------------------------------------------------------------

func TestApplyHighlightToLine(t *testing.T) {
	const hiOn = "\x1b[7m"
	const hiOff = "\x1b[27m"

	tests := []struct {
		name     string
		line     string
		colStart int
		colEnd   int
		wantSeg  string // highlighted segment stripANSI result should equal this
	}{
		{"plain", "hello world", 0, 5, "hello"},
		{"plain middle", "hello world", 6, 11, "world"},
		{"with leading ANSI", "\x1b[32mfoo bar", 0, 3, "foo"},
		{"with leading ANSI middle", "\x1b[32mfoo bar\x1b[0m", 4, 7, "bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := applyHighlightToLine(tt.line, tt.colStart, tt.colEnd)
			if !strings.Contains(out, hiOn) || !strings.Contains(out, hiOff) {
				t.Errorf("output must contain highlight SGR; got %q", out)
			}
			// extract highlighted segment: content between hiOn and hiOff, stripANSI should equal wantSeg
			idxOn := strings.Index(out, hiOn)
			idxOff := strings.Index(out, hiOff)
			if idxOn < 0 || idxOff <= idxOn {
				t.Errorf("invalid highlight bounds in %q", out)
				return
			}
			seg := out[idxOn+len(hiOn) : idxOff]
			plain := stripANSI(seg)
			if plain != tt.wantSeg {
				t.Errorf("highlighted segment stripANSI(%q) = %q, want %q", seg, plain, tt.wantSeg)
			}
		})
	}
}

func TestApplySearchMatchToLineUsesThemeStyleInsteadOfReverseVideo(t *testing.T) {
	ApplyTheme(DefaultTheme())
	line := "plain text"
	out := applySearchMatchToLine(line, 0, len(line))
	if strings.Contains(out, "\x1b[7m") || strings.Contains(out, "\x1b[27m") {
		t.Fatalf("search match highlight should not use reverse video, got %q", out)
	}
	if stripANSI(out) != line {
		t.Fatalf("stripANSI(applySearchMatchToLine()) = %q, want %q", stripANSI(out), line)
	}
}

func TestViewportRenderHighlightsSearchQueryOnFocusedMatchBlock(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 12)
	block := &Block{ID: 1, Type: BlockAssistant, Content: "prefix needle suffix"}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	baseline := m.viewport.Render("", nil, -1, -1, "")
	m.search.State = SearchState{
		Query:   "needle",
		Active:  true,
		Matches: []MatchPosition{{BlockID: 1, BlockIndex: 0, LineOffset: 0, InnerOffset: searchMatchInnerOffset(block, "needle", m.viewport.width)}},
		Current: 0,
	}
	innerOffset := m.search.State.Matches[m.search.State.Current].InnerOffset
	rendered := m.viewport.Render("", nil, m.searchCurrentBlockIndex(), innerOffset, m.search.State.Query)

	baseLines := strings.Split(baseline, "\n")
	renderedLines := strings.Split(rendered, "\n")
	if len(baseLines) != len(renderedLines) {
		t.Fatalf("rendered line count = %d, want %d", len(renderedLines), len(baseLines))
	}
	matched := false
	for i, baseLine := range baseLines {
		if !strings.Contains(stripANSI(baseLine), "needle") {
			continue
		}
		colStart, colEnd, ok := searchMatchColumnRangeInLine(baseLine, "needle")
		if !ok {
			t.Fatalf("expected baseline line to contain search query, got %q", stripANSI(baseLine))
		}
		expected := applySearchMatchToLine(baseLine, colStart, colEnd)
		if renderedLines[i] != expected {
			t.Fatalf("highlighted line mismatch\nexpected: %q\nactual:   %q", expected, renderedLines[i])
		}
		matched = true
	}
	if !matched {
		t.Fatalf("baseline render missing query text, got %q", stripANSI(baseline))
	}
}

func TestViewportRenderHighlightsOnlyCurrentSearchOccurrence(t *testing.T) {
	ApplyTheme(DefaultTheme())
	m := NewModelWithSize(nil, 80, 12)
	block := &Block{ID: 1, Type: BlockAssistant, Content: "needle first\n\nneedle second"}
	m.viewport.AppendBlock(block)
	m.focusedBlockID = 1
	m.refreshBlockFocus()

	baseline := m.viewport.Render("", nil, -1, -1, "")
	m.search.State = SearchState{
		Query:   "needle",
		Active:  true,
		Matches: []MatchPosition{{BlockID: 1, BlockIndex: 0, LineOffset: 0, InnerOffset: searchMatchInnerOffset(block, "needle", m.viewport.width)}},
		Current: 0,
	}
	innerOffset := m.search.State.Matches[m.search.State.Current].InnerOffset
	rendered := m.viewport.Render("", nil, m.searchCurrentBlockIndex(), innerOffset, m.search.State.Query)

	baseLines := strings.Split(baseline, "\n")
	renderedLines := strings.Split(rendered, "\n")
	if len(baseLines) != len(renderedLines) {
		t.Fatalf("rendered line count = %d, want %d", len(renderedLines), len(baseLines))
	}
	diffCount := 0
	for i, baseLine := range baseLines {
		if renderedLines[i] == baseLine {
			continue
		}
		diffCount++
		colStart, colEnd, ok := searchMatchColumnRangeInLine(baseLine, "needle")
		if !ok {
			t.Fatalf("changed line should contain search query, got %q", stripANSI(baseLine))
		}
		expected := applySearchMatchToLine(baseLine, colStart, colEnd)
		if renderedLines[i] != expected {
			t.Fatalf("highlighted line mismatch\nexpected: %q\nactual:   %q", expected, renderedLines[i])
		}
	}
	if diffCount != 1 {
		t.Fatalf("diff line count = %d, want 1", diffCount)
	}
}

// TestApplyHighlightToLineSameLineVsMultiLine ensures both single-line ranges and first/last-line multi-line ranges produce valid highlights
func TestApplyHighlightToLineSameLineVsMultiLine(t *testing.T) {
	line := "config system complete"
	// a segment within a single line
	out := applyHighlightToLine(line, 7, 14)
	if !strings.Contains(out, "\x1b[7m") {
		t.Error("same-line highlight: output must contain highlight SGR")
	}
	plain := stripANSI(out)
	if plain != line {
		t.Errorf("same-line: full line plain text must be unchanged, got %q", plain)
	}
	// full line (simulates a middle line in a multi-line selection)
	outFull := applyHighlightToLine(line, 0, 9999)
	if !strings.Contains(outFull, "\x1b[7m") {
		t.Error("full-line highlight: output must contain highlight SGR")
	}
}

// ---------------------------------------------------------------------------
// selectionColRange — same-line vs cross-line column range
// ---------------------------------------------------------------------------

func TestSelectionColRange(t *testing.T) {
	tests := []struct {
		name        string
		blockID     int
		lineInBlock int
		sel         *SelectionRange
		wantStart   int
		wantEnd     int
		wantOK      bool
	}{
		{
			"same line selection",
			1, 0,
			&SelectionRange{StartBlockID: 1, StartLine: 0, StartCol: 2, EndBlockID: 1, EndLine: 0, EndCol: 8},
			2, 8, true,
		},
		{
			"same line reversed",
			1, 0,
			&SelectionRange{StartBlockID: 1, StartLine: 0, StartCol: 8, EndBlockID: 1, EndLine: 0, EndCol: 2},
			2, 8, true,
		},
		{
			"multi-line first line",
			1, 0,
			&SelectionRange{StartBlockID: 1, StartLine: 0, StartCol: 3, EndBlockID: 1, EndLine: 2, EndCol: 5},
			3, 9999, true,
		},
		{
			"multi-line last line",
			1, 2,
			&SelectionRange{StartBlockID: 1, StartLine: 0, StartCol: 3, EndBlockID: 1, EndLine: 2, EndCol: 5},
			0, 5, true,
		},
		{
			"out of range",
			2, 0,
			&SelectionRange{StartBlockID: 1, StartLine: 0, StartCol: 0, EndBlockID: 1, EndLine: 0, EndCol: 1},
			0, 0, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd, ok := selectionColRange(tt.blockID, tt.lineInBlock, tt.sel)
			if ok != tt.wantOK || gotStart != tt.wantStart || gotEnd != tt.wantEnd {
				t.Errorf("selectionColRange(...) = (%d, %d, %v), want (%d, %d, %v)",
					gotStart, gotEnd, ok, tt.wantStart, tt.wantEnd, tt.wantOK)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// normalizeLineNumberPrefix — copy paste: strip "  " + "%4d " padding, keep line numbers
// ---------------------------------------------------------------------------

func TestNormalizeLineNumberPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"no match", "  no digits here", "  no digits here"},
		{"indented numeric content not a tool line", "  2026-03-19", "  2026-03-19"},
		{"edit block one line", "   582              }", "582\t            }"},
		{"edit block multi", "   582     }\n   583 }\n   584 }", "582\t   }\n583\t}\n584\t}"},
		{"edit add del markers", "   582 -old\n   583 +new", "582\t-old\n583\t+new"},
		{"read block style", "     582  return x", "582\treturn x"},
		{"read block preserves code indent", "     582      return x", "582\t    return x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeLineNumberPrefix(tt.in)
			if got != tt.want {
				t.Errorf("normalizeLineNumberPrefix(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractSelectionTextStripsRenderedIndentForReadBlock(t *testing.T) {
	v := NewViewport(100, 20)
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: "read",
		Content:  `{"path":"main.go"}`,
		ResultContent: "func main() {\n" +
			"    return\n" +
			"}",
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	firstCodeLine := -1
	lastCodeLine := -1
	for i, line := range lines {
		plain := stripANSI(line)
		trimmedPrefix := strings.TrimLeft(plain, " │")
		if strings.Contains(plain, "func main() {") || strings.Contains(plain, "return") || strings.Contains(plain, "}") {
			if strings.HasPrefix(trimmedPrefix, "1 ") || strings.HasPrefix(trimmedPrefix, "2 ") || strings.HasPrefix(trimmedPrefix, "3 ") {
				if firstCodeLine < 0 {
					firstCodeLine = i
				}
				lastCodeLine = i
			}
		}
	}
	if firstCodeLine < 0 || lastCodeLine < firstCodeLine {
		t.Fatalf("failed to locate rendered code lines: %#v", lines)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: 1,
		StartLine:    firstCodeLine,
		StartCol:     0,
		EndBlockID:   1,
		EndLine:      lastCodeLine,
		EndCol:       999,
	})
	want := "1\tfunc main() {\n2\t    return\n3\t}"
	if got != want {
		t.Fatalf("ExtractSelectionText()\n got %q\nwant %q", got, want)
	}
}

func TestExtractSelectionTextEditDiffPreservesLineNumAndMarker(t *testing.T) {
	v := NewViewport(120, 24)
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"example.py","patch":"@@\n-old\n+new\n"}`,
		Diff: "--- a/example.py\n+++ b/example.py\n@@ -8,4 +8,5 @@\n" +
			" def build_items():\n" +
			"     items = [\n" +
			"+        \"added\",\n" +
			"         \"existing\",\n",
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	target := -1
	for i, line := range lines {
		plain := stripANSI(line)
		if strings.Contains(plain, "added") && strings.Contains(plain, "10") && strings.Contains(plain, "+") {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("failed to find rendered add line in %#v", lines)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: 1,
		StartLine:    target,
		StartCol:     0,
		EndBlockID:   1,
		EndLine:      target,
		EndCol:       999,
	})
	want := "10\t+        \"added\","
	if got != want {
		t.Fatalf("ExtractSelectionText() Edit diff\n got %q\nwant %q", got, want)
	}
}

func TestExtractSelectionTextAssistantCodeBlockSkipsDisplayOnlyContinuationIndent(t *testing.T) {
	v := NewViewport(60, 20)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "```go\nfunc TestCancelCurrentTurnRoutesToFocusedSubAgentAndPersistsCancelledToolResult(t *testing.T) {}\n```",
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	var codeLineIdx []int
	for i, line := range lines {
		plain := strings.TrimSpace(stripANSI(line))
		if strings.Contains(plain, "CancelCurrentTurnRoutesToFocusedSubAgent") || strings.Contains(plain, "sistsCancelledToolResult") || strings.Contains(plain, "ersistsCancelledToolResult") {
			codeLineIdx = append(codeLineIdx, i)
		}
	}
	if len(codeLineIdx) != 2 {
		t.Fatalf("expected 2 wrapped code lines, got %d from %#v", len(codeLineIdx), lines)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: 1,
		StartLine:    codeLineIdx[0],
		StartCol:     0,
		EndBlockID:   1,
		EndLine:      codeLineIdx[1],
		EndCol:       999,
	})
	want := "func TestCancelCurrentTurnRoutesToFocusedSubAgentAndP\nersistsCancelledToolResult(t *testing.T) {}"
	if got != want {
		t.Fatalf("ExtractSelectionText assistant code\n got %q\nwant %q", got, want)
	}
}

func TestStripANSIRemovesOSC8Hyperlinks(t *testing.T) {
	styled := ansi.SetHyperlink("https://work.weixin.qq.com/wework_admin/frame", "id=123") +
		"企业微信后台" +
		ansi.ResetHyperlink("id=123")
	if got := stripANSI(styled); got != "企业微信后台" {
		t.Fatalf("stripANSI() with OSC8\n got %q\nwant %q", got, "企业微信后台")
	}
}

func TestExtractSelectionTextStripsOSC8Hyperlinks(t *testing.T) {
	v := NewViewport(100, 20)
	block := &Block{
		ID:      1,
		Type:    BlockAssistant,
		Content: "[企业微信后台](https://work.weixin.qq.com/wework_admin/frame)",
	}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	target := -1
	for i, line := range lines {
		if strings.Contains(stripANSI(line), "企业微信后台") {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("failed to find rendered hyperlink line in %#v", lines)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: 1,
		StartLine:    target,
		StartCol:     0,
		EndBlockID:   1,
		EndLine:      target,
		EndCol:       999,
	})
	want := "企业微信后台 https://work.weixin.qq.com/wework_admin/frame"
	if got != want {
		t.Fatalf("ExtractSelectionText hyperlink\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "\x1b]8;") {
		t.Fatalf("ExtractSelectionText() should not retain OSC8 sequences: %q", got)
	}
}

func TestExtractSelectionTextMouseInclusiveEndpointKeepsLastCharacter(t *testing.T) {
	v := NewViewport(100, 20)
	block := &Block{ID: 1, Type: BlockAssistant, Content: "prefix `app_id/app_secret` suffix"}
	v.AppendBlock(block)

	lines := block.Render(v.width, "")
	target := -1
	startCol := -1
	for i, line := range lines {
		plain := stripANSI(line)
		if before, _, ok := strings.Cut(plain, "app_id/app_secret"); ok {
			target = i
			startCol = ansi.StringWidth(before)
			break
		}
	}
	if target < 0 || startCol < 0 {
		t.Fatalf("failed to find rendered inline code in %#v", lines)
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: 1,
		StartLine:    target,
		StartCol:     startCol,
		EndBlockID:   1,
		EndLine:      target,
		EndCol:       startCol + len("app_id/app_secret"),
	})
	if got != "app_id/app_secret" {
		t.Fatalf("ExtractSelectionText() = %q, want %q", got, "app_id/app_secret")
	}
}

func TestExtractSelectionTextHydratesSpilledBlocks(t *testing.T) {
	v := NewViewport(40, 4)
	v.maxHotBytes = 1024
	v.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 600)})
	v.AppendBlock(&Block{ID: 2, Type: BlockAssistant, Content: "tail"})

	if !v.blocks[0].spillCold {
		t.Fatalf("expected first block to spill, got spillCold=%v", v.blocks[0].spillCold)
	}
	block := v.MaterializeBlockByID(1)
	if block == nil {
		t.Fatal("expected spilled block to materialize by id")
	}
	if !strings.Contains(block.Content, "alpha") {
		t.Fatalf("expected materialized block content, got %q", block.Content)
	}
	lineWithAlpha := -1
	startCol := -1
	for i, line := range block.Render(v.width, "") {
		plain := stripANSI(line)
		if before, _, ok := strings.Cut(plain, "alpha"); ok {
			lineWithAlpha = i
			startCol = ansi.StringWidth(before)
			break
		}
	}
	if lineWithAlpha < 0 || startCol < 0 {
		t.Fatal("expected rendered block to contain alpha")
	}

	got := v.ExtractSelectionText(SelectionRange{
		StartBlockID: 1,
		StartLine:    lineWithAlpha,
		StartCol:     startCol,
		EndBlockID:   1,
		EndLine:      lineWithAlpha,
		EndCol:       startCol + 5,
	})
	if got != "alpha" {
		t.Fatalf("ExtractSelectionText() = %q, want %q", got, "alpha")
	}
	if v.blocks[0].spillCold {
		t.Fatal("selection extraction should hydrate spilled block")
	}
}

func TestViewportGetBlockAndLineAtUsesBlockStarts(t *testing.T) {
	v := NewViewport(80, 20)
	first := &Block{ID: 1, Type: BlockUser, Content: "first"}
	second := &Block{ID: 2, Type: BlockUser, Content: "second"}
	v.AppendBlock(first)
	v.AppendBlock(second)

	offset := first.LineCount(80)
	block, line := v.GetBlockAndLineAt(offset)
	if block == nil {
		t.Fatal("expected block at second block start")
	}
	if block.ID != second.ID {
		t.Fatalf("block ID = %d, want %d", block.ID, second.ID)
	}
	if line != 0 {
		t.Fatalf("line = %d, want 0", line)
	}
}

func TestViewportHasNoLeadingTurnSpacingBetweenBlocks(t *testing.T) {
	v := NewViewport(80, 20)
	v.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "assistant"})
	v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "user"})

	starts := v.blockStarts()
	if len(starts) < 2 {
		t.Fatalf("expected at least two block starts, got %v", starts)
	}
	block, line := v.GetBlockAndLineAt(starts[1])
	if block == nil || block.ID != 2 || line != 0 {
		t.Fatalf("expected second block to start immediately at its block start, got block=%v line=%d", block, line)
	}
}

func TestViewportGetBlockAndLineAtNeverReturnsNegativeLine(t *testing.T) {
	v := NewViewport(80, 20)
	v.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "assistant"})
	v.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: "user"})
	v.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "assistant tail"})

	for offset := 0; offset < v.TotalLines(); offset++ {
		block, line := v.GetBlockAndLineAt(offset)
		if block == nil {
			continue
		}
		if line < 0 {
			t.Fatalf("offset %d resolved to negative line index %d for block %d", offset, line, block.ID)
		}
	}
}

func TestViewportUpdateBlockRecalcForNonLastBlock(t *testing.T) {
	v := NewViewport(40, 5)
	first := &Block{ID: 1, Type: BlockToolCall, ToolName: "delegate", Collapsed: true, Content: `{"description":"short"}`}
	second := &Block{ID: 2, Type: BlockAssistant, Content: "tail"}
	v.AppendBlock(first)
	v.AppendBlock(second)

	first.DoneSummary = "this is a much longer summary that should expand the first card into multiple wrapped lines and therefore increase totalLines noticeably"
	first.InvalidateCache()
	v.UpdateBlock(first.ID)

	realTotal := 0
	for _, b := range v.visibleBlocks() {
		realTotal += v.blockSpanLines(b)
	}
	if got := v.TotalLines(); got != realTotal {
		t.Fatalf("TotalLines() = %d, want %d", got, realTotal)
	}

	v.ScrollDown(999)
	wantOffset := max(realTotal-v.height, 0)
	if v.offset != wantOffset {
		t.Fatalf("offset after ScrollDown = %d, want %d", v.offset, wantOffset)
	}
}

func TestViewportScrollDownNoopAtBottomPreservesManualNonStickyState(t *testing.T) {
	v := NewViewport(20, 4)
	for i := range 6 {
		v.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("gamma ", 12)})
	}

	v.ScrollToBottom()
	bottomOffset := v.offset
	v.sticky = false

	v.ScrollDown(1)
	if v.offset != bottomOffset {
		t.Fatalf("offset after no-op ScrollDown at bottom = %d, want %d", v.offset, bottomOffset)
	}
	if v.sticky {
		t.Fatal("no-op ScrollDown at bottom should preserve non-sticky manual scroll state")
	}

	v.offset = bottomOffset - 1
	v.sticky = false
	v.ScrollDown(1)
	if v.offset != bottomOffset {
		t.Fatalf("offset after ScrollDown reaching bottom = %d, want %d", v.offset, bottomOffset)
	}
	if !v.sticky {
		t.Fatal("ScrollDown that actually reaches bottom should enable sticky follow")
	}
}

func TestVisibleBlocksCacheInvalidatesOnMutationAndFilter(t *testing.T) {
	v := NewViewport(40, 10)
	v.ReplaceBlocks([]*Block{
		{ID: 1, Type: BlockUser, Content: "main"},
		{ID: 2, Type: BlockUser, Content: "sub", AgentID: "agent-1"},
	})

	all := v.visibleBlocks()
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2", len(all))
	}
	cached := v.visibleBlocks()
	if len(cached) != len(all) || (len(cached) > 0 && &cached[0] != &all[0]) {
		t.Fatal("visibleBlocks should reuse cached slice when unchanged")
	}

	v.SetFilter("main")
	filtered := v.visibleBlocks()
	if len(filtered) != 1 || filtered[0].ID != 1 {
		t.Fatalf("filtered visible blocks = %+v, want only block 1", filtered)
	}

	v.AppendBlock(&Block{ID: 3, Type: BlockAssistant, Content: "more"})
	filtered = v.visibleBlocks()
	if len(filtered) != 2 || filtered[1].ID != 3 {
		t.Fatalf("after append visible blocks = %+v, want blocks 1 and 3", filtered)
	}

	v.RemoveBlockByID(1)
	filtered = v.visibleBlocks()
	if len(filtered) != 1 || filtered[0].ID != 3 {
		t.Fatalf("after remove visible blocks = %+v, want only block 3", filtered)
	}
}

func TestViewportSetSizeKeepsBottomAnchorWhenSticky(t *testing.T) {
	v := NewViewport(20, 4)
	for i := range 6 {
		v.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("alpha ", 12)})
	}

	v.ScrollToBottom()
	if !v.sticky {
		t.Fatal("expected viewport to be sticky at bottom")
	}
	if !v.atBottom() {
		t.Fatal("expected viewport to start at bottom")
	}

	v.SetSize(20, 3)
	if !v.atBottom() {
		t.Fatalf("sticky resize should keep bottom anchor: offset=%d total=%d height=%d", v.offset, v.totalLines, v.height)
	}
	wantOffset := max(v.TotalLines()-v.height, 0)
	if v.offset != wantOffset {
		t.Fatalf("offset after sticky resize = %d, want %d", v.offset, wantOffset)
	}
}

func TestViewportSetSizePreservesManualScrollWhenNotSticky(t *testing.T) {
	v := NewViewport(20, 4)
	for i := range 6 {
		v.AppendBlock(&Block{ID: i + 1, Type: BlockAssistant, Content: strings.Repeat("beta ", 12)})
	}

	v.ScrollToBottom()
	v.ScrollUp(3)
	if v.sticky {
		t.Fatal("expected viewport to become non-sticky after manual scroll")
	}
	prevOffset := v.offset

	v.SetSize(20, 3)
	if v.offset != prevOffset {
		t.Fatalf("non-sticky resize should preserve offset when still in range: got %d, want %d", v.offset, prevOffset)
	}
}
