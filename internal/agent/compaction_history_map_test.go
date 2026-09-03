package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestEvidenceItemTopicsExtractsBoundedContentMap(t *testing.T) {
	items := []evidenceItem{
		{Title: "Latest Done rejection", Excerpt: "reject", Priority: 100, Sequence: 5},
		{Title: "User correction / constraint", Excerpt: "约束", Priority: 100, Sequence: 6},
		{Title: "Go build failure in internal/llm", Excerpt: "undefined: foo", Priority: 95, Sequence: 4},
		{Title: "Go build failure in internal/llm", Excerpt: "duplicate", Priority: 95, Sequence: 3},
		{Title: "Latest user request", Excerpt: "继续", Priority: 90, Sequence: 7},
		{Title: "SubAgent completion summary", Excerpt: "done", Priority: 85, Sequence: 2},
		{Title: "Recent code diff in main.go", Excerpt: "diff", Priority: 80, Sequence: 1},
		{Title: "No excerpt item", Excerpt: "", Priority: 200, Sequence: 8},
	}
	topics := evidenceItemTopics(items)
	joined := strings.Join(topics, "\n")
	if strings.Contains(joined, "Latest Done rejection") || strings.Contains(joined, "User correction") ||
		strings.Contains(joined, "Latest user request") || strings.Contains(joined, "SubAgent completion") {
		t.Fatalf("noise titles leaked into the content map: %v", topics)
	}
	if strings.Contains(joined, "No excerpt") {
		t.Fatalf("item without excerpt leaked into the content map: %v", topics)
	}
	if strings.Count(joined, "Go build failure") != 1 {
		t.Fatalf("duplicate titles not deduplicated: %v", topics)
	}
	if topics[0] != "Go build failure in internal/llm" {
		t.Fatalf("highest-priority actionable title should lead the map, got %v", topics)
	}
	if len(topics) > 12 {
		t.Fatalf("content map unbounded: %d topics", len(topics))
	}
}

func TestFormatHistoryMapLinesPairsPathWithTopics(t *testing.T) {
	sessionDir := t.TempDir()
	a := newTestMainAgent(t, sessionDir)
	a.sessionDir = sessionDir

	// Export two generations so both carry topics in their status metadata.
	if _, _, _, err := a.exportCompactionHistory([]message.Message{{Role: message.RoleUser, Content: "u1"}}, 2, []string{"first topic"}, a.captureCompactionArchiveMeta()); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.exportCompactionHistory([]message.Message{{Role: message.RoleUser, Content: "u2"}}, 3, []string{"second topic", "third topic"}, a.captureCompactionArchiveMeta()); err != nil {
		t.Fatal(err)
	}
	refs, err := listHistoryReferences(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	metas := readCompactionHistoryMetas(refs)
	lines := formatHistoryMapLines(refs, metas)
	if len(lines) != 2 {
		t.Fatalf("map lines = %d, want 2 (%v)", len(lines), lines)
	}
	if !strings.Contains(lines[0], "history-2.md") || !strings.Contains(lines[0], "first topic") {
		t.Fatalf("line 0 missing path+topics: %q", lines[0])
	}
	if !strings.Contains(lines[1], "history-3.md") || !strings.Contains(lines[1], "second topic") || !strings.Contains(lines[1], "third topic") {
		t.Fatalf("line 1 missing path+topics: %q", lines[1])
	}
	if !filepath.IsAbs(strings.TrimPrefix(strings.SplitN(lines[0], ":", 2)[0], "~")) && !strings.HasPrefix(lines[0], "~") {
		t.Fatalf("map line should keep a stable (possibly abbreviated) address: %q", lines[0])
	}
}

func TestFormatHistoryMapLinesToleratesMissingMeta(t *testing.T) {
	sessionDir := t.TempDir()
	refs := []string{filepath.Join(sessionDir, "history-9.md"), filepath.Join(sessionDir, "history-10.md")}
	lines := formatHistoryMapLines(refs, nil)
	if len(lines) != 2 || !strings.Contains(lines[0], "history-9.md") || strings.Contains(lines[0], ": ") {
		t.Fatalf("missing meta should fall back to a plain path line, got %v", lines)
	}
}
