package tools

import (
	"context"
	"testing"
)

func TestApplyPatchHunksPreserveLineEndings(t *testing.T) {
	ctx := func(s string) applyPatchLine { return applyPatchLine{Kind: ' ', Text: s} }
	del := func(s string) applyPatchLine { return applyPatchLine{Kind: '-', Text: s} }
	add := func(s string) applyPatchLine { return applyPatchLine{Kind: '+', Text: s} }
	tests := []struct {
		name    string
		content string
		hunks   []applyPatchHunk
		want    string
	}{
		{
			name:    "uniform crlf",
			content: "a\r\nb\r\nc\r\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{ctx("a"), del("b"), add("x"), add("y")}}},
			want:    "a\r\nx\r\ny\r\nc\r\n",
		},
		{
			name:    "uniform cr",
			content: "a\rb\rc\r",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{ctx("a"), del("b"), add("x")}}},
			want:    "a\rx\rc\r",
		},
		{
			name:    "mixed keeps untouched lines",
			content: "a\r\nb\nc\rd\r\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{ctx("b"), del("c"), add("x")}}},
			want:    "a\r\nb\nx\rd\r\n",
		},
		{
			name:    "mixed added lines follow the replaced line",
			content: "a\nb\r\nc\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{del("b"), add("x"), add("y")}}},
			want:    "a\nx\r\ny\r\nc\n",
		},
		{
			name:    "mixed insertion takes the following line's ending",
			content: "a\nb\r\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{add("x"), ctx("b")}}},
			want:    "a\nx\r\nb\r\n",
		},
		{
			name:    "mixed deletion",
			content: "a\r\nb\nc\r\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{del("b")}}},
			want:    "a\r\nc\r\n",
		},
		{
			name:    "mixed append at end",
			content: "a\nb\r\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{add("c")}}},
			want:    "a\nb\r\nc\r\n",
		},
		{
			name:    "missing final newline is terminated like the line before",
			content: "a\r\nb\nc",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{ctx("c"), add("d")}}},
			want:    "a\r\nb\nc\nd\n",
		},
		{
			name:    "lone cr in a crlf file splits lines",
			content: "a\r\nb\rc\r\n",
			hunks:   []applyPatchHunk{{Lines: []applyPatchLine{del("b"), add("x")}}},
			want:    "a\r\nx\rc\r\n",
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
