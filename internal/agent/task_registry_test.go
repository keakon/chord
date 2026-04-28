package agent

import (
	"testing"

	"github.com/keakon/chord/internal/tools"
)

func TestWriteScopesOverlapMatchesExactAndNestedPathsOnly(t *testing.T) {
	tests := []struct {
		name string
		a    tools.WriteScope
		b    tools.WriteScope
		want bool
	}{
		{
			name: "same file overlaps",
			a:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			b:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			want: true,
		},
		{
			name: "file under path prefix overlaps",
			a:    tools.WriteScope{Files: []string{"internal/foo/bar.go"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			want: true,
		},
		{
			name: "nested path prefixes overlap",
			a:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo/bar"}},
			want: true,
		},
		{
			name: "prefix-like sibling names do not overlap",
			a:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{Files: []string{"internal/foobar/baz.go"}},
			want: false,
		},
		{
			name: "path prefixes require boundary",
			a:    tools.WriteScope{PathPrefix: []string{"pkg/mod"}},
			b:    tools.WriteScope{PathPrefix: []string{"pkg/module"}},
			want: false,
		},
		{
			name: "readonly never overlaps",
			a:    tools.WriteScope{ReadOnly: true, PathPrefix: []string{"internal/foo"}},
			b:    tools.WriteScope{PathPrefix: []string{"internal/foo"}},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := writeScopesOverlap(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("writeScopesOverlap(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestPathContainsPathUsesPathBoundaries(t *testing.T) {
	if !pathContainsPath("internal/foo", "internal/foo/bar.go") {
		t.Fatal("expected nested path to match")
	}
	if pathContainsPath("internal/foo", "internal/foobar/bar.go") {
		t.Fatal("did not expect prefix-like sibling path to match")
	}
	if !pathContainsPath("internal/foo", "internal/foo") {
		t.Fatal("expected identical path to match")
	}
}
