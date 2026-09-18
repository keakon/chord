package memory

import (
	"strings"
	"testing"
)

// The byte cap must police the escaped wire form: a summary dense in `&` (and
// the other characters JSON escapes) would otherwise inject several times the
// cap while the raw text looked bounded.
func TestBoundedSummaryCapsEscapedWireBytes(t *testing.T) {
	idx := &MemoryIndex{Managed: []ManagedEntry{{
		ID:      "big-entry",
		Link:    ".chord/memory/records/big-entry.md",
		Summary: strings.Repeat("&", 20000),
	}}}
	summary, active := BoundedSummary(idx)
	if !active {
		t.Fatal("expected an active summary for a single managed entry")
	}
	if got := escapedWireLen(summary); got > maxSummaryBytes {
		t.Fatalf("escaped wire length = %d, want <= %d", got, maxSummaryBytes)
	}
}

// Content that needs no escaping and fits the budget must come through intact.
func TestBoundedSummaryLeavesShortContentIntact(t *testing.T) {
	idx := &MemoryIndex{Managed: []ManagedEntry{{
		ID:      "plain-entry",
		Link:    ".chord/memory/records/plain-entry.md",
		Summary: "plain summary",
	}}}
	summary, active := BoundedSummary(idx)
	if !active {
		t.Fatal("expected an active summary")
	}
	if !strings.Contains(summary, "plain summary") {
		t.Fatalf("summary = %q, want the entry summary intact", summary)
	}
}
