package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func compactionCheckpointFixture() string {
	return message.CompactionSummaryHeader +
		message.CompactionAnchorsOpenTag +
		"Original request:\n- review the unpushed commits\n" +
		message.CompactionAnchorsCloseTag +
		"\n\n## Current User Request\n- analyze the feedback\n\n## Next Step\n- ship it" +
		message.CompactionCompressedTag +
		"\nEarlier conversation was compacted into the summary above.\n" +
		"Archived history files:\n- history-1.md\n\n" +
		message.CompactionEvidenceTag +
		"Verbatim excerpts preserved for the immediate continuation.\n\n1. Latest user request\nExcerpt:\nanalyze the feedback"
}

// legacyCheckpointFixture mirrors a checkpoint persisted by a build that still
// appended the display-hint tail, to pin down how old content is handled.
func legacyCheckpointFixture() string {
	return compactionCheckpointFixture() +
		message.CompactionDisplayHint +
		"Press toggle-collapse to expand and inspect the full preserved context message."
}

// TestCompactionCardKeepsProtocolMarkersOffProseLines requires that the
// checkpoint's protocol markers do not end up merged into the prose that
// follows them. A bare "[Session Anchors]" line has no block-level Markdown
// meaning, so rendering the checkpoint as a single Markdown document collapsed
// "[Context Summary]", "[Session Anchors]" and "Original request:" onto one
// line.
func TestCompactionCardKeepsProtocolMarkersOffProseLines(t *testing.T) {
	block := &Block{ID: 0, Type: BlockCompactionSummary, Content: compactionCheckpointFixture()}
	plain := stripANSI(strings.Join(block.Render(100, ""), "\n"))

	for _, marker := range []string{"[Context Summary]", "[Session Anchors]", "[/Session Anchors]"} {
		if strings.Contains(plain, marker) {
			t.Fatalf("protocol marker %q leaked into the rendered card:\n%s", marker, plain)
		}
	}
	if !strings.Contains(plain, "CONTEXT SUMMARY #1") {
		t.Fatalf("card label missing:\n%s", plain)
	}
	for _, want := range []string{
		compactionAnchorsSectionLabel,
		"Original request:",
		"review the unpushed commits",
		"analyze the feedback",
		compactionArchiveSectionLabel,
		"history-1.md",
		compactionEvidenceSectionLabel,
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered card missing %q:\n%s", want, plain)
		}
	}
	// "Original request:" must start its own line rather than trailing a marker.
	// Card lines carry a border prefix, so compare after trimming it.
	for line := range strings.SplitSeq(plain, "\n") {
		trimmed := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "│ "))
		if strings.Contains(trimmed, "Original request:") && !strings.HasPrefix(trimmed, "Original request:") {
			t.Fatalf("anchor prose merged into another line: %q", trimmed)
		}
	}
}

func TestSplitCompactionSectionsOrdersRegions(t *testing.T) {
	sections := splitCompactionSections(compactionCheckpointFixture())
	var labels []string
	for _, section := range sections {
		labels = append(labels, section.label)
	}
	want := []string{
		compactionAnchorsSectionLabel,
		"",
		compactionArchiveSectionLabel,
		compactionEvidenceSectionLabel,
	}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("labels = %v, want %v", labels, want)
		}
	}
	if !strings.Contains(sections[0].body, "Original request:") {
		t.Fatalf("anchors section body = %q", sections[0].body)
	}
	if strings.Contains(sections[1].body, "[Context") {
		t.Fatalf("summary body still carries a marker: %q", sections[1].body)
	}
}

// TestSplitCompactionSectionsDropsLegacyDisplayHintTail requires that a
// display-hint tail persisted by an older build is cut off instead of rendered:
// the checkpoint card is always fully expanded, so the hint is stale text that
// must not surface as its own region or leak into the evidence section.
func TestSplitCompactionSectionsDropsLegacyDisplayHintTail(t *testing.T) {
	sections := splitCompactionSections(legacyCheckpointFixture())
	var labels []string
	for _, section := range sections {
		labels = append(labels, section.label)
	}
	want := []string{
		compactionAnchorsSectionLabel,
		"",
		compactionArchiveSectionLabel,
		compactionEvidenceSectionLabel,
	}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for _, section := range sections {
		if strings.Contains(section.body, "toggle-collapse") || strings.Contains(section.body, "[Context display hint]") {
			t.Fatalf("legacy hint text leaked into section %q: %q", section.label, section.body)
		}
	}
}

// TestSplitCompactionSectionsPassesThroughPlainContent requires that content
// carrying no markers renders exactly as before, as one unlabelled region.
func TestSplitCompactionSectionsPassesThroughPlainContent(t *testing.T) {
	sections := splitCompactionSections("## Goal\n- keep going")
	if len(sections) != 1 || sections[0].label != "" {
		t.Fatalf("sections = %+v, want one unlabelled section", sections)
	}
	if sections[0].body != "## Goal\n- keep going" {
		t.Fatalf("body = %q", sections[0].body)
	}
	if got := splitCompactionSections("   "); got != nil {
		t.Fatalf("blank content = %+v, want nil", got)
	}
}

// TestCompactionSectionHighlighterCacheSurvivesReRender requires that each
// checkpoint section holds its own highlighter slot. A shared slot would be fed
// a different sample per section on every render, resetting the lexer cache
// once per section and dropping the cross-frame reuse that richMarkdownHL exists
// for.
func TestCompactionSectionHighlighterCacheSurvivesReRender(t *testing.T) {
	content := message.CompactionSummaryHeader +
		message.CompactionAnchorsOpenTag +
		"Original request:\n- keep the parser fast\n\n```go\npackage p\n```\n" +
		message.CompactionAnchorsCloseTag +
		"\n\n## Current User Request\n- analyze\n\n```go\npackage q\n```\n\n## Next Step\n- ship" +
		message.CompactionCompressedTag + "\nEarlier conversation was compacted.\n" +
		message.CompactionEvidenceTag + "Verbatim excerpts.\n\n1. Latest failing tool result\nExcerpt:\n```text\nError: boom\n```" +
		message.CompactionDisplayHint + "Press toggle-collapse."
	b := &Block{ID: 0, Type: BlockCompactionSummary, Content: content}
	b.Render(100, "")
	slots := b.compactionSectionHL
	if len(slots) < 2 {
		t.Fatalf("expected at least two per-section highlighter slots, got %d", len(slots))
	}
	if slots[0] == nil || slots[1] == nil {
		t.Fatalf("slots 0/1 not populated: %v", slots)
	}
	if slots[0] == slots[1] {
		t.Fatal("sections share one highlighter slot")
	}
	first := reflect.ValueOf(slots[0].renderCache).Pointer()
	if first == 0 {
		t.Fatal("anchors highlighter cache not initialized after first render")
	}
	b.Render(100, "")
	if again := reflect.ValueOf(b.compactionSectionHL[0].renderCache).Pointer(); again != first {
		t.Fatal("re-render reset the anchors highlighter cache; sections must hold disjointslots")
	}
}
