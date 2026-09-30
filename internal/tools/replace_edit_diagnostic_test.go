package tools

import (
	"strings"
	"testing"
)

func TestEditBatchDetailRequiresReadForOmittedText(t *testing.T) {
	for _, tc := range []struct {
		name, content, old, read string
	}{
		{"two differing lines", "item 1 y\nitem 2 y\nitem 3 stable\n", "item 1 x\nitem 2 x\nitem 3 stable\n", "offset=1 limit=3"},
		{"three differing lines", "item 1 y\nitem 2 y\nitem 3 y\n", "item 1 x\nitem 2 x\nitem 3 x\n", "offset=1 limit=3"},
		{"clipped line", strings.Repeat("a", maxToolLineRunes+10) + "y\n", strings.Repeat("a", maxToolLineRunes+10) + "x\n", "offset=1 limit=1"},
		{"line shift", "item 1 stable content\n\nitem 2 stable content\nitem 3 stable content\n", "item 1 stable content\nitem 2 stable content\nitem 3 stable content\n", "offset=1 limit=3"},
		{"CRLF block", "item 1 y\r\nitem 2 y\r\n", "item 1 x\r\nitem 2 x\r\n", "offset=1 limit=2"},
		{"CR block", "item 1 y\ritem 2 y\r", "item 1 x\ritem 2 x\r", "offset=1 limit=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := editClosestMatchDetail(tc.content, tc.old, maxBatchEntryDetailLines)
			if len(detail) != maxBatchEntryDetailLines {
				t.Fatalf("detail = %q, want %d lines", detail, maxBatchEntryDetailLines)
			}
			if !strings.Contains(detail[2], "read the file with "+tc.read) {
				t.Fatalf("detail lacks the bounded read: %q", detail)
			}
		})
	}
}

func TestEditBatchDetailShowsCompleteSingleDifference(t *testing.T) {
	detail := editClosestMatchDetail("alpha beta\n", "alpha zeta\n", maxBatchEntryDetailLines)
	if len(detail) != maxBatchEntryDetailLines || !strings.Contains(detail[1], `"alpha beta"`) || !strings.Contains(detail[2], `"alpha zeta"`) {
		t.Fatalf("single difference should be shown as a complete line pair: %q", detail)
	}
}

func TestEditBatchDetailNeedsWholeDetailBudget(t *testing.T) {
	for budget := range maxBatchEntryDetailLines {
		if detail := editClosestMatchDetail("alpha beta\n", "alpha zeta\n", budget); len(detail) != 0 {
			t.Fatalf("budget %d yielded an incomplete diagnostic: %q", budget, detail)
		}
	}
}

func TestEditSingleDiagnosticKeepsMultipleCompleteDifferences(t *testing.T) {
	content := "item 1 y\nitem 2 y\nitem 3 y\n"
	old := "item 1 x\nitem 2 x\nitem 3 x\n"
	diagnostic := editClosestMatchDiagnostic(content, old)
	for _, want := range []string{`"item 1 y"`, `"item 2 y"`, `"item 3 y"`, "Rebuild old_string from the file lines above"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("full single-edit report lost %q:\n%s", want, diagnostic)
		}
	}
}
