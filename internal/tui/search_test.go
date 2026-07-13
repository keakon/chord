package tui

import (
	"strings"
	"testing"

	tea "github.com/keakon/bubbletea/v2"

	"github.com/keakon/chord/internal/tools"
)

func TestFindMatchesAtWidthSearchesToolAndResultText(t *testing.T) {
	blocks := []*Block{
		{Type: BlockAssistant, Content: "plain text"},
		{Type: BlockToolCall, ToolName: "shell", Content: `{"command":"echo hi"}`, ResultContent: "needle from result"},
	}

	matches := FindMatchesAtWidth(blocks, "needle", 80)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].BlockIndex != 1 {
		t.Fatalf("BlockIndex = %d, want 1", matches[0].BlockIndex)
	}
}

func TestFindMatchesAtWidthSearchesThinking(t *testing.T) {
	blocks := []*Block{
		{
			Type:          BlockAssistant,
			Content:       "assistant body",
			ThinkingParts: []string{"internal needle note"},
		},
	}

	thinkingMatches := FindMatchesAtWidth(blocks, "internal needle note", 80)
	if len(thinkingMatches) != 1 {
		t.Fatalf("thinkingMatches = %d, want 1", len(thinkingMatches))
	}
}

func TestFindMatchesAtWidthSearchesStructuredVisibleText(t *testing.T) {
	blocks := []*Block{
		{Type: BlockThinking, Content: "original", ThinkingTranslations: []ThinkingTranslationView{{Content: "translated needle"}}},
		{Type: BlockUser, Content: "attachments", ImageParts: []BlockImagePart{{FileName: "diagram-needle.png"}}, PDFNames: []string{"needle.pdf"}, FileRefs: []string{"docs/needle.md"}},
		{Type: BlockUser, UserLocalShellCmd: "echo needle", UserLocalShellResult: "needle output"},
		{Type: BlockToolCall, ToolName: tools.NameEdit, Content: `{"path":"example.go"}`, Diff: "@@ -1 +1 @@\n-old\n+diff needle"},
		{Type: BlockToolCall, ToolName: tools.NameDone, DoneReport: "## Report\nreport needle"},
		{Type: BlockToolCall, ToolName: tools.NameDelegate, DoneSummary: "summary needle", ResultDone: true},
	}

	for _, query := range []string{"translated needle", "diagram-needle.png", "needle.pdf", "docs/needle.md", "echo needle", "needle output", "diff needle", "report needle", "summary needle"} {
		if matches := FindMatchesAtWidth(blocks, query, 100); len(matches) != 1 {
			t.Errorf("FindMatchesAtWidth(%q) returned %d matches, want 1", query, len(matches))
		}
	}
}

func TestFindMatchesAtWidthSkipsTruncatedAttachmentText(t *testing.T) {
	ApplyTheme(DefaultTheme())
	const (
		query = "hidden-needle"
		width = 30
	)
	longPrefix := strings.Repeat("a", 80)
	cases := []struct {
		name  string
		block *Block
	}{
		{name: "image", block: &Block{ID: 1, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: longPrefix + query + ".png"}}}},
		{name: "pdf", block: &Block{ID: 1, Type: BlockUser, PDFNames: []string{longPrefix + query + ".pdf"}}},
		{name: "file reference", block: &Block{ID: 1, Type: BlockUser, FileRefs: []string{longPrefix + query + ".md"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if matches := FindMatchesAtWidth([]*Block{tc.block}, query, width); len(matches) != 0 {
				t.Fatalf("FindMatchesAtWidth() returned %d matches for truncated text, want 0", len(matches))
			}
		})
	}
}

func TestFindMatchesAtWidthKeepsVisibleAttachmentPrefix(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: "visible-needle-" + strings.Repeat("a", 80) + ".png"}}}

	matches := FindMatchesAtWidth([]*Block{block}, "visible-needle", 30)
	if len(matches) != 1 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches for visible prefix, want 1", len(matches))
	}
}

func TestFindMatchesAtWidthUsesVisibleToolArguments(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{
		ID:       1,
		Type:     BlockToolCall,
		ToolName: tools.NameEdit,
		Content:  `{"path":"visible-needle.go","patch":"@@\n-hidden-needle\n+replacement\n"}`,
	}

	if matches := FindMatchesAtWidth([]*Block{block}, "visible-needle.go", 80); len(matches) != 1 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches for visible tool path, want 1", len(matches))
	}
	if matches := FindMatchesAtWidth([]*Block{block}, "hidden-needle", 80); len(matches) != 0 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches for hidden tool argument, want 0", len(matches))
	}
}

func TestDeferredStartupTranscriptSearchSkipsTruncatedAttachmentText(t *testing.T) {
	ApplyTheme(DefaultTheme())
	const (
		query = "hidden-needle"
		width = 30
	)
	block := &Block{ID: 1, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: strings.Repeat("a", 80) + query + ".png"}}}
	m := NewModelWithSize(nil, width, 24)
	m.startupDeferredTranscript = &startupDeferredTranscriptState{
		allBlocks: []*Block{block},
		blockMeta: buildStartupDeferredBlockMeta([]*Block{block}, width),
	}

	if matches := m.deferredStartupTranscriptSearch(query); len(matches) != 0 {
		t.Fatalf("deferredStartupTranscriptSearch() returned %d matches for truncated text, want 0", len(matches))
	}
}

func TestDeferredStartupTranscriptSearchValidatesSpilledVisibleText(t *testing.T) {
	ApplyTheme(DefaultTheme())
	const width = 40
	cases := []struct {
		name  string
		query string
		block *Block
	}{
		{
			name:  "hidden assistant markdown",
			query: "hidden-needle",
			block: &Block{ID: 1, Type: BlockAssistant, Content: "visible <!-- hidden-needle --> phrase"},
		},
		{
			name:  "truncated attachment label",
			query: "hidden-needle",
			block: &Block{ID: 1, Type: BlockUser, ImageCount: 1, ImageParts: []BlockImagePart{{FileName: strings.Repeat("a", 80) + "hidden-needle.png"}}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModelWithSize(nil, width, 24)
			m.viewport.AppendBlock(tc.block)
			meta := buildStartupDeferredBlockMeta([]*Block{tc.block}, width)
			if !m.viewport.spillBlock(tc.block) {
				t.Fatal("expected spillBlock to succeed")
			}
			m.startupDeferredTranscript = &startupDeferredTranscriptState{
				allBlocks: []*Block{tc.block},
				blockMeta: meta,
			}

			if matches := m.deferredStartupTranscriptSearch(tc.query); len(matches) != 0 {
				t.Fatalf("deferredStartupTranscriptSearch() returned %d invisible matches, want 0", len(matches))
			}
			if !tc.block.spillCold {
				t.Fatal("search visibility inspection should keep source block cold")
			}
		})
	}
}

func TestFindMatchesAtWidthSkipsDiagnosticArtifactBlocks(t *testing.T) {
	blocks := []*Block{
		{Type: BlockToolCall, ToolName: "read", Content: `{"path":"~/.local/state/chord/logs/tui-dumps/tui-dump.log","limit":120}`, ResultContent: "1\tfind artifact line", ResultDone: true},
		{Type: BlockToolCall, ToolName: "shell", Content: `{"command":"rg -n find internal/tui"}`, ResultContent: "real find result", ResultDone: true},
	}

	matches := FindMatchesAtWidth(blocks, "find", 80)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].BlockIndex != 1 {
		t.Fatalf("BlockIndex = %d, want 1 after skipping diagnostic artifact block", matches[0].BlockIndex)
	}
}

func TestFindMatchesAtWidthSkipsInvisibleThinkingBlocks(t *testing.T) {
	blocks := []*Block{
		{Type: BlockThinking, Content: ""},
		{Type: BlockToolCall, ToolName: "shell", Content: `{"command":"echo hi"}`, ResultContent: "needle from result", ResultDone: true},
	}

	matches := FindMatchesAtWidth(blocks, "needle", 80)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].BlockIndex != 1 {
		t.Fatalf("BlockIndex = %d, want 1 after skipping invisible thinking block", matches[0].BlockIndex)
	}
}

func TestRevealSearchMatchedBlockExpandsToolContent(t *testing.T) {
	localShell := &Block{Type: BlockUser, UserLocalShellCmd: "echo ok", UserLocalShellResult: "deep needle output", Collapsed: true}
	if !revealSearchMatchedBlock(localShell) || localShell.Collapsed {
		t.Fatal("local shell card should expand when revealed by search")
	}

	thinking := &Block{Type: BlockThinking, Content: "deep needle thought", ThinkingCollapsed: true}
	if !revealSearchMatchedBlock(thinking) || thinking.ThinkingCollapsed {
		t.Fatal("thinking card should expand when revealed by search")
	}

	tool := &Block{Type: BlockToolCall, ToolName: "read", Collapsed: true, ResultContent: "1\tneedle\n2\tother"}
	if !revealSearchMatchedBlock(tool) {
		t.Fatal("read tool reveal should report changed state")
	}
	if tool.Collapsed {
		t.Fatal("read tool should expand when revealed by search")
	}
	if !tool.ReadContentExpanded {
		t.Fatal("read tool should show full content when revealed by search")
	}

	generic := &Block{Type: BlockToolCall, ToolName: "shell", ToolCallDetailExpanded: false, ResultContent: "needle from result", ResultDone: false, Collapsed: true}
	if !revealSearchMatchedBlock(generic) {
		t.Fatal("generic tool reveal should report changed state")
	}
	if !generic.ToolCallDetailExpanded {
		t.Fatal("generic tool should expand detail when revealed by search")
	}
	if generic.Collapsed {
		t.Fatal("generic tool with result should expand card when revealed by search")
	}
	if !generic.ResultDone {
		t.Fatal("generic tool with result should be forced to terminal rendering when revealed by search")
	}
	if generic.SettledAt.IsZero() {
		t.Fatal("generic tool forced done by search should set SettledAt to freeze elapsed rendering")
	}

	filePatch := &Block{Type: BlockToolCall, ToolName: tools.NameEdit, Collapsed: true, ResultContent: "done"}
	if !revealSearchMatchedBlock(filePatch) {
		t.Fatal("edit tool reveal should report changed state")
	}
	if filePatch.Collapsed {
		t.Fatal("edit tool should expand when revealed by search")
	}

	summary := &Block{
		Type:                 BlockCompactionSummary,
		Collapsed:            true,
		Content:              "## Goal\n- preview only",
		CompactionSummaryRaw: "[Context Summary]\n## Goal\n- preview only\n\n## Files and Evidence\n- docs/architecture/context-management.md\n\n[Context compressed]\nArchived history files:\n- history-1.md",
	}
	if !revealSearchMatchedBlock(summary) {
		t.Fatal("compaction summary reveal should report changed state")
	}
	if summary.Collapsed {
		t.Fatal("compaction summary should expand when revealed by search")
	}
	if !strings.Contains(summary.Content, "docs/architecture/context-management.md") {
		t.Fatalf("expanded compaction summary content = %q, want full raw content", summary.Content)
	}
}

func TestFindMatchesAtWidthRevealsWarmCollapsedToolCache(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameRead, Collapsed: true, ResultDone: true, ResultContent: "1\tpreview\n2\tdeep needle"}
	_ = block.Render(80, "")

	matches := FindMatchesAtWidth([]*Block{block}, "deep needle", 80)
	if len(matches) != 1 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches for warm collapsed tool cache, want 1", len(matches))
	}
	if block.ReadContentExpanded || !block.Collapsed {
		t.Fatal("search matching should not mutate the original collapsed tool block")
	}
}

func TestApproximateSearchMatchInnerOffsetForReadResult(t *testing.T) {
	block := &Block{Type: BlockToolCall, ToolName: "read", ResultContent: "1\talpha\n2\tbeta\n3\tneedle line\n4\tdelta"}
	if got := approximateSearchMatchInnerOffset(block, "needle line", 80); got <= 0 {
		t.Fatalf("approximateSearchMatchInnerOffset() = %d, want > 0 for later line", got)
	}
}

func TestWrappedSearchMatchLineOffsetMatchesFullWrapping(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		query string
		width int
	}{
		{name: "first line", text: "alpha needle omega", query: "needle", width: 80},
		{name: "wrapped words", text: "alpha beta gamma needle omega tail", query: "needle", width: 12},
		{name: "query spans words", text: "alpha beta gamma needle phrase omega", query: "needle phrase", width: 24},
		{name: "long word", text: "alpha supercalifragilisticneedleword omega", query: "needle", width: 10},
		{name: "indented multiline", text: "  alpha beta gamma\n    delta needle omega\ntrailing text", query: "needle", width: 14},
		{name: "unicode", text: "前缀 内容 很长\n    目标needle后缀 尾部", query: "needle", width: 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			textLower := strings.ToLower(tt.text)
			queryLower := strings.ToLower(tt.query)
			want := -1
			for i, line := range wrapText(textLower, tt.width) {
				if strings.Contains(line, queryLower) {
					want = i
					break
				}
			}
			got, ok := wrappedSearchMatchLineOffset(textLower, queryLower, tt.width)
			if want < 0 {
				if ok {
					t.Fatalf("wrappedSearchMatchLineOffset() = (%d, true), want no match", got)
				}
				return
			}
			if !ok || got != want {
				t.Fatalf("wrappedSearchMatchLineOffset() = (%d, %v), want (%d, true)", got, ok, want)
			}
		})
	}
}

func TestRenderedSearchMatchInnerOffsetUsesRenderedToolLayout(t *testing.T) {
	block := &Block{
		Type:                BlockToolCall,
		ToolName:            "read",
		Content:             `{"path":"internal/tui/app.go","limit":20,"offset":0}`,
		ResultContent:       "1\talpha\n2\tbeta\n3\tgamma\n4\tdelta\n5\tepsilon\n6\tzeta\n7\teta\n8\ttheta\n9\tiota\n10\tkappa\n11\tneedle line\n12\tomega",
		ResultDone:          true,
		Collapsed:           false,
		ReadContentExpanded: true,
	}
	if got := renderedSearchMatchInnerOffset(block, "needle line", 80); got < 10 {
		t.Fatalf("renderedSearchMatchInnerOffset() = %d, want >= 10 for deep rendered line", got)
	}
}

func TestFindMatchesAtWidthSearchesVisibleAssistantMarkdown(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockAssistant, Content: "visible **needle** phrase"}

	matches := FindMatchesAtWidth([]*Block{block}, "visible needle", 80)
	if len(matches) != 1 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches, want 1", len(matches))
	}
	lines := block.Render(80, "")
	if matches[0].InnerOffset < 0 || matches[0].InnerOffset >= len(lines) {
		t.Fatalf("InnerOffset = %d, rendered lines = %d", matches[0].InnerOffset, len(lines))
	}
	if _, _, ok := searchMatchColumnRangeInLine(lines[matches[0].InnerOffset], "visible needle"); !ok {
		t.Fatal("visible assistant Markdown match cannot be highlighted")
	}
}

func TestFindMatchesAtWidthSearchesVisibleAssistantHTMLEntity(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockAssistant, Content: "visible a &amp; b phrase"}

	matches := FindMatchesAtWidth([]*Block{block}, "a & b", 80)
	if len(matches) != 1 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches, want 1", len(matches))
	}
	lines := block.Render(80, "")
	if matches[0].InnerOffset < 0 || matches[0].InnerOffset >= len(lines) {
		t.Fatalf("InnerOffset = %d, rendered lines = %d", matches[0].InnerOffset, len(lines))
	}
	if _, _, ok := searchMatchColumnRangeInLine(lines[matches[0].InnerOffset], "a & b"); !ok {
		t.Fatal("visible assistant HTML entity match cannot be highlighted")
	}
}

func TestFindMatchesAtWidthSkipsHiddenAssistantMarkdown(t *testing.T) {
	ApplyTheme(DefaultTheme())
	block := &Block{ID: 1, Type: BlockAssistant, Content: "visible <!-- hidden-needle --> phrase"}

	if matches := FindMatchesAtWidth([]*Block{block}, "hidden-needle", 80); len(matches) != 0 {
		t.Fatalf("FindMatchesAtWidth() returned %d matches for hidden Markdown, want 0", len(matches))
	}
}

func TestAssistantMarkdownMayContainQueryAcrossFormatting(t *testing.T) {
	tests := []struct {
		content string
		query   string
	}{
		{content: "visible **needle** phrase", query: "visible needle"},
		{content: "visible **needle** phrase", query: "needle phrase"},
		{content: "visible [needle](https://example.com) phrase", query: "needle phrase"},
		{content: "visible `needle` phrase", query: "visible needle"},
		{content: "visible a &amp; b phrase", query: "a & b"},
		{content: "可见 **目标** 文本", query: "可见 目标"},
	}
	for _, tt := range tests {
		if !assistantMarkdownMayContainQuery(tt.content, strings.ToLower(tt.query)) {
			t.Fatalf("assistantMarkdownMayContainQuery(%q, %q) = false, want true", tt.content, tt.query)
		}
	}
	if assistantMarkdownMayContainQuery("visible **needle** phrase", "needle missing") {
		t.Fatal("assistantMarkdownMayContainQuery() matched characters not present in order")
	}
}

func TestAssistantMarkdownMayContainQueryLongCommonPrefix(t *testing.T) {
	content := strings.Repeat("a", 100_000)
	query := strings.Repeat("a", 1_000) + "b"
	if assistantMarkdownMayContainQuery(content, query) {
		t.Fatal("assistantMarkdownMayContainQuery() matched absent long-prefix query")
	}
}

func TestStructuredDisplaySearchMatchesAreRenderedAndHighlightable(t *testing.T) {
	ApplyTheme(DefaultTheme())
	cases := []struct {
		name  string
		query string
		block *Block
	}{
		{
			name:  "thinking translation",
			query: "translated needle",
			block: &Block{ID: 1, Type: BlockThinking, Content: "original thought", ThinkingTranslations: []ThinkingTranslationView{{Content: "translated needle"}}, ThinkingCollapsed: true},
		},
		{
			name:  "edit diff",
			query: "diff needle",
			block: &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameEdit, Content: `{"path":"example.go"}`, Diff: "--- a/example.go\n+++ b/example.go\n@@ -1 +1 @@\n-old\n+diff needle", ResultDone: true},
		},
		{
			name:  "done report",
			query: "report needle",
			block: &Block{ID: 1, Type: BlockToolCall, ToolName: tools.NameDone, DoneReport: "## Report\nreport needle", ResultDone: true},
		},
		{
			name:  "pdf attachment",
			query: "needle.pdf",
			block: &Block{ID: 1, Type: BlockUser, Content: "attachment", PDFNames: []string{"needle.pdf"}},
		},
		{
			name:  "image attachment",
			query: "diagram-needle.png",
			block: &Block{ID: 1, Type: BlockUser, Content: "attachment", ImageParts: []BlockImagePart{{FileName: "diagram-needle.png"}}},
		},
		{
			name:  "local shell result",
			query: "result needle",
			block: &Block{ID: 1, Type: BlockUser, UserLocalShellCmd: "echo ok", UserLocalShellResult: "result needle", Collapsed: true},
		},
		{
			name:  "compaction raw content",
			query: "archive needle",
			block: &Block{ID: 1, Type: BlockCompactionSummary, Collapsed: true, Content: "preview", CompactionSummaryRaw: "[Context Summary]\npreview\n\narchive needle\n\n[Context compressed]"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			revealSearchMatchedBlock(tc.block)
			lines := tc.block.Render(120, "")
			matchedLine := -1
			for i, line := range lines {
				if strings.Contains(strings.ToLower(stripANSI(line)), strings.ToLower(tc.query)) {
					matchedLine = i
					break
				}
			}
			if matchedLine < 0 {
				t.Fatalf("rendered block does not contain query %q", tc.query)
			}
			if got := renderedSearchMatchInnerOffset(tc.block, tc.query, 120); got != matchedLine {
				t.Fatalf("renderedSearchMatchInnerOffset() = %d, want %d", got, matchedLine)
			}
			colStart, colEnd, ok := searchMatchColumnRangeInLine(lines[matchedLine], tc.query)
			if !ok {
				t.Fatalf("searchMatchColumnRangeInLine() did not find %q", tc.query)
			}
			highlighted := applySearchMatchToLine(lines[matchedLine], colStart, colEnd)
			if highlighted == lines[matchedLine] || stripANSI(highlighted) != stripANSI(lines[matchedLine]) {
				t.Fatalf("search highlight was not applied without changing visible text")
			}
		})
	}
}

func TestSearchableTextLowerCacheInvalidates(t *testing.T) {
	block := &Block{Type: BlockAssistant, Content: "first value"}
	if got := block.searchableTextLower(); got != "first value" {
		t.Fatalf("searchableTextLower() = %q, want %q", got, "first value")
	}
	block.Content = "second value"
	block.InvalidateCache()
	if got := block.searchableTextLower(); got != "second value" {
		t.Fatalf("searchableTextLower() after invalidate = %q, want %q", got, "second value")
	}
}

func TestSearchableTextLowerUsesCompactionSummaryRaw(t *testing.T) {
	block := &Block{
		Type:                 BlockCompactionSummary,
		Content:              "preview",
		CompactionSummaryRaw: "[Context Summary]\n## Goal\n- continue\n\n## Files and Evidence\n- docs/architecture/context-management.md\n\n[Context compressed]\nArchived history files:\n- history-1.md",
	}
	if got := block.searchableTextLower(); !strings.Contains(got, "docs/architecture/context-management.md") {
		t.Fatalf("searchableTextLower() = %q, want raw compaction summary content", got)
	}
}

func TestSearchPillStatusShowsAcrossModesWhileSearchSessionActive(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.search.State.Active = true
	m.search.State.Query = "grep"
	m.search.State.Matches = []MatchPosition{{BlockIndex: 0}, {BlockIndex: 1}, {BlockIndex: 2}}
	m.search.State.Current = 1

	m.mode = ModeNormal
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [2/3]") {
		t.Fatalf("normal-mode status bar should show active search session, got %q", plain)
	}
	m.mode = ModeInsert
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [2/3]") {
		t.Fatalf("insert-mode status bar should show active search session, got %q", plain)
	}
	m.mode = ModeSearch
	if plain := stripANSI(m.renderStatusBar()); !strings.Contains(plain, "/grep [2/3]") {
		t.Fatalf("search-mode status bar should show active search session, got %q", plain)
	}
}

func TestSearchBackspaceExitsWhenInputIsAlreadyEmpty(t *testing.T) {
	for _, key := range []tea.Key{
		{Code: tea.KeyBackspace},
		{Code: 'h', Mod: tea.ModCtrl},
	} {
		m := NewModelWithSize(nil, 120, 24)
		m.mode = ModeSearch
		m.search = NewSearchModel(ModeNormal)

		m.handleSearchKey(tea.KeyPressMsg(key))

		if m.mode != ModeNormal {
			t.Fatalf("key %q left mode = %v, want ModeNormal", tea.KeyPressMsg(key).String(), m.mode)
		}
	}
}

func TestSearchBackspaceDeletesLastCharacterBeforeExiting(t *testing.T) {
	m := NewModelWithSize(nil, 120, 24)
	m.mode = ModeSearch
	m.search = NewSearchModel(ModeNormal)
	m.search.Input.SetValue("a")
	backspace := tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})

	m.handleSearchKey(backspace)

	if got := m.search.Input.Value(); got != "" {
		t.Fatalf("input after first backspace = %q, want empty", got)
	}
	if m.mode != ModeSearch {
		t.Fatalf("mode after deleting last character = %v, want ModeSearch", m.mode)
	}

	m.handleSearchKey(backspace)

	if m.mode != ModeNormal {
		t.Fatalf("mode after backspace on empty input = %v, want ModeNormal", m.mode)
	}
}
