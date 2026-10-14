package markdownrender

import (
	"slices"
	"testing"
)

func TestNormalizeRenderedMarkdownIndentPreservesSourceIndent(t *testing.T) {
	for _, tc := range []struct {
		lines, want []string
	}{
		{[]string{"  first", "", "   second"}, []string{"first", "", " second"}},
		{[]string{"    code", "      nested"}, []string{"    code", "      nested"}},
		{[]string{"plain", "  nested"}, []string{"plain", "  nested"}},
		{[]string{"", "  "}, []string{"", "  "}},
	} {
		if got := normalizeRenderedMarkdownIndent(slices.Clone(tc.lines)); !slices.Equal(got, tc.want) {
			t.Errorf("normalizeRenderedMarkdownIndent(%q) = %q, want %q", tc.lines, got, tc.want)
		}
	}
}
