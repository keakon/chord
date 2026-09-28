package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestApplyPatchHunksReplaceBoundaries(t *testing.T) {
	plus := func(text string) applyPatchLine { return applyPatchLine{Kind: '+', Text: text} }
	minus := func(text string) applyPatchLine { return applyPatchLine{Kind: '-', Text: text} }
	ctx := func(text string) applyPatchLine { return applyPatchLine{Kind: ' ', Text: text} }
	tests := []struct {
		name    string
		content string
		hunks   []applyPatchHunk
		want    string
	}{
		{
			name:    "delete only line leaves empty file",
			content: "a\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{minus("a")}}},
			want:    "",
		},
		{
			name:    "head replace with long tail",
			content: "a\nb\nc\nd\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{minus("a"), plus("z")}}},
			want:    "z\nb\nc\nd\n",
		},
		{
			name:    "context insert in middle",
			content: "a\nb\nc\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{ctx("a"), plus("x"), ctx("b")}}},
			want:    "a\nx\nb\nc\n",
		},
		{
			name:    "pure delete shrinks file",
			content: "a\nb\nc\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{minus("b")}}},
			want:    "a\nc\n",
		},
		{
			name:    "missing trailing newline input",
			content: "a\nb",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{minus("b"), plus("z")}}},
			want:    "a\nz\n",
		},
		{
			name:    "eof hunk pins to tail",
			content: "a\nb\nc\n",
			hunks:   []applyPatchHunk{{EndOfFile: true, Lines: []applyPatchLine{minus("c"), plus("z")}}},
			want:    "a\nb\nz\n",
		},
		{
			name:    "crlf preserved across hunks",
			content: "a\r\nb\r\nc\r\n",
			hunks: []applyPatchHunk{
				{Lines: []applyPatchLine{minus("a"), plus("x")}},
				{Lines: []applyPatchLine{minus("c"), plus("y")}},
			},
			want: "x\r\nb\r\ny\r\n",
		},
		{
			// The running total peaks at +2 before the later hunks shrink the
			// file back, so the replacements run on the pre-grown slice.
			name:    "grow then shrink",
			content: "a\nb\nc\nd\n",
			hunks: []applyPatchHunk{
				{Lines: []applyPatchLine{minus("a"), plus("x"), plus("y"), plus("z")}},
				{Lines: []applyPatchLine{minus("b")}},
				{Lines: []applyPatchLine{minus("d")}},
			},
			want: "x\ny\nz\nc\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, _, _, err := applyApplyPatchHunks(context.Background(), tt.content, tt.hunks)
			if err != nil {
				t.Fatalf("applyApplyPatchHunks error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("applyApplyPatchHunks = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyPatchHunksPeakGrowth(t *testing.T) {
	hunks := []applyPatchHunk{
		{Lines: []applyPatchLine{{Kind: ' ', Text: "ctx"}, {Kind: '-', Text: "old"}, {Kind: '+', Text: "new"}, {Kind: '+', Text: "added"}}},
		{Lines: []applyPatchLine{{Kind: ' ', Text: "ctx"}, {Kind: '-', Text: "a"}, {Kind: '-', Text: "b"}, {Kind: '+', Text: "c"}}},
		{Lines: []applyPatchLine{{Kind: ' ', Text: "ctx"}, {Kind: '-', Text: "old"}, {Kind: '+', Text: "new"}}},
		{Lines: []applyPatchLine{{Kind: '+', Text: "appended"}}},
	}
	// +1, -1, 0, +1: net +1 and the running total never exceeds it.
	if got := applyPatchHunksPeakGrowth(hunks); got != 1 {
		t.Fatalf("applyPatchHunksPeakGrowth = %d, want 1", got)
	}
	if got := applyPatchHunksPeakGrowth(nil); got != 0 {
		t.Fatalf("applyPatchHunksPeakGrowth(nil) = %d, want 0", got)
	}
	// Grow then shrink: the peak (+2) is what the pre-grow must cover, while
	// the net delta falls back to zero.
	mixed := []applyPatchHunk{
		{Lines: []applyPatchLine{{Kind: '-', Text: "a"}, {Kind: '+', Text: "b"}, {Kind: '+', Text: "c"}, {Kind: '+', Text: "d"}}},
		{Lines: []applyPatchLine{{Kind: '-', Text: "e"}}},
		{Lines: []applyPatchLine{{Kind: '-', Text: "f"}}},
	}
	if got := applyPatchHunksPeakGrowth(mixed); got != 2 {
		t.Fatalf("applyPatchHunksPeakGrowth(mixed) = %d, want 2", got)
	}
}

// TestApplyApplyPatchHunksManyHunksEquivalence covers the in-place replacement
// with 50+ ordered hunks and asserts byte-identical output against a manually
// applied expectation.
func TestApplyApplyPatchHunksManyHunksEquivalence(t *testing.T) {
	const totalLines = 5000
	const hunkCount = 60
	var content strings.Builder
	for i := range totalLines {
		fmt.Fprintf(&content, "line %06d\n", i)
	}
	lineAt := func(i int) string { return fmt.Sprintf("line %06d", i) }

	step := (totalLines - 200) / hunkCount
	hunks := make([]applyPatchHunk, 0, hunkCount)
	wantLines := make([]string, totalLines)
	for i := range totalLines {
		wantLines[i] = lineAt(i)
	}
	// Apply expectations in reverse so earlier indices stay valid while
	// building the want slice; the implementation applies forward with
	// searchStart, which must yield the same bytes.
	type edit struct {
		pos     int
		oldLen  int
		newText []string
	}
	var edits []edit
	for h := range hunkCount {
		pos := 100 + h*step
		hunks = append(hunks, applyPatchHunk{Lines: []applyPatchLine{
			{Kind: ' ', Text: lineAt(pos)},
			{Kind: '-', Text: lineAt(pos + 1)},
			{Kind: '+', Text: lineAt(pos+1) + " changed"},
			{Kind: '+', Text: lineAt(pos+1) + " added"},
		}})
		edits = append(edits, edit{pos: pos + 1, oldLen: 1,
			newText: []string{lineAt(pos+1) + " changed", lineAt(pos+1) + " added"}})
	}
	for i := range slices.Backward(edits) {
		e := edits[i]
		wantLines = append(wantLines[:e.pos], append(e.newText, wantLines[e.pos+e.oldLen:]...)...)
	}
	want := strings.Join(wantLines, "\n") + "\n"

	out, punct, fuzzy, _, err := applyApplyPatchHunks(context.Background(), content.String(), hunks)
	if err != nil {
		t.Fatalf("applyApplyPatchHunks failed: %v", err)
	}
	if punct != 0 || fuzzy != 0 {
		t.Fatalf("punctuation=%d fuzzy=%d, want 0/0", punct, fuzzy)
	}
	if out != want {
		t.Fatalf("output mismatch: got %d bytes, want %d bytes", len(out), len(want))
	}
}

func TestApplyApplyPatchHunksAppendEmptyOldSeq(t *testing.T) {
	out, _, _, _, err := applyApplyPatchHunks(context.Background(), "a\nb\n", []applyPatchHunk{
		{Lines: []applyPatchLine{{Kind: '+', Text: "c"}}},
	})
	if err != nil {
		t.Fatalf("applyApplyPatchHunks failed: %v", err)
	}
	if out != "a\nb\nc\n" {
		t.Fatalf("append output = %q, want %q", out, "a\nb\nc\n")
	}
}
