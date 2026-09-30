package tools

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEditBatchLargeFailureKeepsAllSummariesWithoutDetails(t *testing.T) {
	const source = "item alpha stable\n"
	for _, kind := range []string{"missing", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			edits := make([]textReplacement, 50)
			for i := range edits {
				edits[i].NewString = new("replacement")
				if kind == "missing" {
					edits[i].OldString = fmt.Sprintf("item alpha stale-%02d", i)
				}
			}
			_, err := planExactReplacements(source, edits)
			if err == nil {
				t.Fatal("expected the entire batch to fail")
			}
			message := err.Error()
			previous := -1
			for i := range edits {
				token := fmt.Sprintf("edits[%d]:", i)
				at := strings.Index(message, token)
				if at <= previous || strings.Count(message, token) != 1 {
					t.Fatalf("entry %d is not reported once in index order:\n%s", i, message)
				}
				previous = at
			}
			if strings.Contains(message, "Closest match") || strings.Contains(message, "  file line") {
				t.Fatalf("large report spent a detail budget that does not exist:\n%s", message)
			}
			wantLines := len(edits) + 2
			if kind == "missing" {
				wantLines++
			}
			if got := strings.Count(message, "\n") + 1; got != wantLines {
				t.Fatalf("report lines = %d, want %d summaries and guidance only", got, wantLines)
			}
			if !strings.HasSuffix(message, "Fix the failing entries above and resubmit the complete batch.") {
				t.Fatalf("report lost its retry guidance:\n%s", message)
			}
		})
	}
}

func TestEditBatchRepeatedOverlapsReportEachEntryOnce(t *testing.T) {
	edits := []textReplacement{
		{OldString: "alpha", NewString: new("beta"), ReplaceAll: true},
		{OldString: "alpha", NewString: new("gamma"), ReplaceAll: true},
	}
	_, err := planExactReplacements(strings.Repeat("alpha\n", 50), edits)
	if err == nil {
		t.Fatal("expected overlapping replacements to fail")
	}
	message := err.Error()
	for _, token := range []string{"edits[0]: overlaps edits[1]", "edits[1]: overlaps edits[0]"} {
		if strings.Count(message, token) != 1 {
			t.Fatalf("overlap should name each entry once, missing or repeated %q:\n%s", token, message)
		}
	}
	if got := strings.Count(message, "\n") + 1; got != 4 {
		t.Fatalf("report lines = %d, want header, two entries, footer", got)
	}
}

func TestEditBatchNestedOverlapsExcludeAllConflictingEntries(t *testing.T) {
	const source = "abcdefghij\ndisjoint\n"
	edits := []textReplacement{
		{OldString: "abcdefghij", NewString: new("whole")},
		{OldString: "abc", NewString: new("left")},
		{OldString: "hij", NewString: new("right")},
		{OldString: "missing", NewString: new("replacement")},
		{OldString: "disjoint", NewString: new("changed")},
	}
	_, err := planExactReplacements(source, edits)
	if err == nil {
		t.Fatal("expected nested overlaps and missing text to fail")
	}
	message := err.Error()
	for i := range 3 {
		token := fmt.Sprintf("edits[%d]: overlaps edits[", i)
		if strings.Count(message, token) != 1 {
			t.Fatalf("conflicting entry %d should appear once:\n%s", i, message)
		}
	}
	if !strings.Contains(message, "Remaining entries (edits[4]) passed per-entry matching but were not applied.") {
		t.Fatalf("remaining entries include a conflict or lose the disjoint entry:\n%s", message)
	}
}

func TestEditBatchDetailBudgetNeverCutsOffReadGuidance(t *testing.T) {
	const source = "item 1 y\nitem 2 y\nitem 3 stable\n"
	const old = "item 1 x\nitem 2 x\nitem 3 stable\n"
	for _, count := range []int{33, 34, 35, 36, 37, 38} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			edits := make([]textReplacement, count)
			for i := range edits {
				edits[i] = textReplacement{OldString: old, NewString: new("replacement")}
			}
			_, err := planExactReplacements(source, edits)
			if err == nil {
				t.Fatal("expected missing blocks to fail")
			}
			message := err.Error()
			for i := range edits {
				detail := entryDetailLines(message, i)
				if len(detail) != 0 && (len(detail) != maxBatchEntryDetailLines || !strings.Contains(detail[2], "read the file with offset=1 limit=3")) {
					t.Fatalf("entry %d has incomplete guidance: %q", i, detail)
				}
			}
			if lines := strings.Count(message, "\n") + 1; lines > max(batchReportLineBudget, count+3) {
				t.Fatalf("report exceeds its summary/detail budget: %d lines", lines)
			}
		})
	}
}

func TestEditBatchSkippedTextNeedNotExistAndDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	const source = "alpha\n"
	path := writeEditFixture(t, dir, "sample.txt", source)
	stamp := time.Unix(1000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runEdit(t, dir, map[string]any{"path": path, "edits": []map[string]any{
		{"old_string": "missing", "new_string": "missing"},
	}})
	if err != nil || !strings.Contains(result, "No changes") {
		t.Fatalf("identical missing text should request no change: result=%q, err=%v", result, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("no-op batch wrote the file")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != source {
		t.Fatalf("no-op batch changed the file: %q, %v", got, err)
	}
}
