package tui

import (
	"strings"
	"testing"
)

func TestFindMatchesAtWidthSearchesToolAndResultText(t *testing.T) {
	blocks := []*Block{
		{Type: BlockAssistant, Content: "plain text"},
		{Type: BlockToolCall, ToolName: "Bash", Content: `{"command":"echo hi"}`, ResultContent: "needle from result"},
	}

	matches := FindMatchesAtWidth(blocks, "needle", 80)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].BlockIndex != 1 {
		t.Fatalf("BlockIndex = %d, want 1", matches[0].BlockIndex)
	}
}

func TestFindMatchesAtWidthSearchesImageFilenameAndThinking(t *testing.T) {
	blocks := []*Block{
		{
			Type:          BlockAssistant,
			Content:       "assistant body",
			ImageParts:    []BlockImagePart{{FileName: "diagram-needle.png"}},
			ThinkingParts: []string{"internal needle note"},
		},
	}

	imageMatches := FindMatchesAtWidth(blocks, "diagram-needle", 80)
	if len(imageMatches) != 1 {
		t.Fatalf("imageMatches = %d, want 1", len(imageMatches))
	}
	thinkingMatches := FindMatchesAtWidth(blocks, "internal needle note", 80)
	if len(thinkingMatches) != 1 {
		t.Fatalf("thinkingMatches = %d, want 1", len(thinkingMatches))
	}
}

func TestFindMatchesAtWidthSkipsDiagnosticArtifactBlocks(t *testing.T) {
	blocks := []*Block{
		{Type: BlockToolCall, ToolName: "Read", Content: `{"path":"~/.local/state/chord/logs/tui-dumps/tui-dump.log","limit":120}`, ResultContent: "1\tfind artifact line", ResultDone: true},
		{Type: BlockToolCall, ToolName: "Bash", Content: `{"command":"rg -n find internal/tui"}`, ResultContent: "real find result", ResultDone: true},
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
		{Type: BlockToolCall, ToolName: "Bash", Content: `{"command":"echo hi"}`, ResultContent: "needle from result", ResultDone: true},
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
	tool := &Block{Type: BlockToolCall, ToolName: "Read", Collapsed: true, ResultContent: "1\tneedle\n2\tother"}
	if !revealSearchMatchedBlock(tool) {
		t.Fatal("Read tool reveal should report changed state")
	}
	if tool.Collapsed {
		t.Fatal("Read tool should expand when revealed by search")
	}
	if !tool.ReadContentExpanded {
		t.Fatal("Read tool should show full content when revealed by search")
	}

	generic := &Block{Type: BlockToolCall, ToolName: "Bash", ToolCallDetailExpanded: false, ResultContent: "needle from result", ResultDone: false, Collapsed: true}
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

	fileEdit := &Block{Type: BlockToolCall, ToolName: "Edit", Collapsed: true, ResultContent: "done"}
	if !revealSearchMatchedBlock(fileEdit) {
		t.Fatal("Edit tool reveal should report changed state")
	}
	if fileEdit.Collapsed {
		t.Fatal("Edit tool should expand when revealed by search")
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
}

func TestApproximateSearchMatchInnerOffsetForReadResult(t *testing.T) {
	block := &Block{Type: BlockToolCall, ToolName: "Read", ResultContent: "1\talpha\n2\tbeta\n3\tneedle line\n4\tdelta"}
	if got := approximateSearchMatchInnerOffset(block, "needle line", 80); got <= 0 {
		t.Fatalf("approximateSearchMatchInnerOffset() = %d, want > 0 for later line", got)
	}
}

func TestRenderedSearchMatchInnerOffsetUsesRenderedToolLayout(t *testing.T) {
	block := &Block{
		Type:                BlockToolCall,
		ToolName:            "Read",
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
