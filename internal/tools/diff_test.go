package tools

import (
	"strings"
	"testing"
)

func TestGenerateUnifiedDiffSummaryCountsFullChangesBeforeTruncation(t *testing.T) {
	var oldBuilder strings.Builder
	for range 427 {
		oldBuilder.WriteString("line old\n")
	}
	summary := GenerateUnifiedDiffSummary(oldBuilder.String(), "package tui\n\n// split\n", "confirm.go")
	if summary.Removed < 400 {
		t.Fatalf("Removed = %d, want >= 400", summary.Removed)
	}
	if !strings.Contains(summary.Text, "... (diff truncated)") {
		t.Fatalf("expected truncated diff text, got:\n%s", summary.Text)
	}
}

func TestGenerateUnifiedDiffGroupsDeletesBeforeInsertsForTwoLineReplacement(t *testing.T) {
	old := "lineA old\nlineB old\n"
	new := "lineA new\nlineB new\n"
	out := GenerateUnifiedDiff(old, new, "f.go")
	// Documented layout: consecutive '-' lines then consecutive '+' lines inside the hunk.
	want := "-lineA old\n-lineB old\n+lineA new\n+lineB new\n"
	if !strings.Contains(out, want) {
		t.Fatalf("unexpected hunk body, want substring:\n%s\ngot:\n%s", want, out)
	}
}

func TestInlineDiff(t *testing.T) {
	tests := []struct {
		old, new string
		// oldSegs: concatenate Text where Kind is equal or delete; newSegs: equal or insert
		oldWant, newWant string
	}{
		{"", "", "", ""},
		{"a", "a", "a", "a"},
		{"a", "b", "a", "b"},
		{"ab", "ac", "ab", "ac"},
		{"foo bar", "foo baz", "foo bar", "foo baz"},
		{"if x = nil {", "if err != nil {", "if x = nil {", "if err != nil {"},
	}
	for _, tt := range tests {
		oldSegs, newSegs := InlineDiff(tt.old, tt.new)
		oldGot := concatSegs(oldSegs, "equal", "delete")
		newGot := concatSegs(newSegs, "equal", "insert")
		if oldGot != tt.oldWant || newGot != tt.newWant {
			t.Errorf("InlineDiff(%q, %q): old %q want %q, new %q want %q",
				tt.old, tt.new, oldGot, tt.oldWant, newGot, tt.newWant)
		}
	}
}

func TestTokenAwareInlineDiffKeepsIdentifierInsertionContiguous(t *testing.T) {
	oldSegs, newSegs := InlineDiff("myVariable", "myHTTPVariable")
	if got := concatSegKinds(newSegs); got != "equal,insert,equal" {
		t.Fatalf("unexpected new segment kinds: %s", got)
	}
	if got := concatSegs(newSegs, "insert"); got != "HTTP" {
		t.Fatalf("inserted text = %q, want %q", got, "HTTP")
	}
	if got := concatSegs(newSegs, "equal"); got != "myVariable" {
		t.Fatalf("equal text = %q, want %q", got, "myVariable")
	}
	if concatSegs(oldSegs, "delete") != "" {
		t.Fatalf("expected no deletes in old segments, got %#v", oldSegs)
	}
}

func TestTokenAwareInlineDiffRefinesWithinChangedToken(t *testing.T) {
	oldSegs, newSegs := InlineDiff("timeout=30", "timeout_ms=30")
	if got := concatSegKinds(oldSegs); got != "equal" {
		t.Fatalf("expected old token to stay equal for pure insertion, got kinds %s", got)
	}
	if got := concatSegs(newSegs, "insert"); got != "_ms" {
		t.Fatalf("insert text = %q, want %q", got, "_ms")
	}
	if got := concatSegKinds(newSegs); got != "equal,insert,equal" {
		t.Fatalf("expected refined new token diff, got kinds %s", got)
	}
}

func TestTokenAwareInlineDiffHandlesFunctionArgumentExpansion(t *testing.T) {
	oldSegs, newSegs := InlineDiff("foo(bar, baz)", "foo(longBar, baz)")
	inserted := concatSegs(newSegs, "insert")
	if !strings.Contains(inserted, "long") {
		t.Fatalf("insert text = %q, want to contain %q", inserted, "long")
	}
	newEqual := concatSegs(newSegs, "equal")
	if !strings.Contains(newEqual, "foo(") || !strings.Contains(newEqual, ", baz)") {
		t.Fatalf("expected surrounding callsite context to remain equal, got %#v", newSegs)
	}
	oldCombined := concatSegs(oldSegs, "equal", "delete")
	if oldCombined != "foo(bar, baz)" {
		t.Fatalf("old combined text = %q, want %q", oldCombined, "foo(bar, baz)")
	}
}

func TestTokenAwareInlineDiffHandlesPathSegmentDeletion(t *testing.T) {
	oldSegs, newSegs := InlineDiff("github.com/org/service/internal/api", "github.com/org/service/api")
	deleted := concatSegs(oldSegs, "delete")
	if !strings.Contains(deleted, "internal") {
		t.Fatalf("delete text = %q, want to contain %q", deleted, "internal")
	}
	if got := concatSegs(newSegs, "insert"); got != "" {
		t.Fatalf("expected no insertions for pure path deletion, got %q", got)
	}
}

func TestInlineDiffKeepsGraphemeClustersAtomic(t *testing.T) {
	for _, tt := range []struct {
		name string
		old  string
		new  string
	}{
		{name: "skin tone", old: "👍", new: "👍🏽"},
		{name: "zwj sequence", old: "👨", new: "👨‍👩"},
		{name: "combining mark", old: "e", new: "e\u0301"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldSegs, newSegs := InlineDiff(tt.old, tt.new)
			if got := concatSegs(oldSegs, "equal", "delete"); got != tt.old {
				t.Fatalf("old combined text = %q, want %q", got, tt.old)
			}
			if got := concatSegs(newSegs, "equal", "insert"); got != tt.new {
				t.Fatalf("new combined text = %q, want %q", got, tt.new)
			}
			for _, seg := range newSegs {
				if seg.Kind == "equal" {
					t.Fatalf("new grapheme was split into an equal segment: %#v", newSegs)
				}
			}
		})
	}
}

func concatSegKinds(segs []InlineSegment) string {
	kinds := make([]string, 0, len(segs))
	for _, s := range segs {
		kinds = append(kinds, s.Kind)
	}
	return strings.Join(kinds, ",")
}

func concatSegs(segs []InlineSegment, kinds ...string) string {
	set := make(map[string]bool)
	for _, k := range kinds {
		set[k] = true
	}
	var b strings.Builder
	for _, s := range segs {
		if set[s.Kind] {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}
