package agent

import (
	"testing"
)

// TestAnchorLineStripsForgedHeading verifies anchorLine cannot be tricked into
// re-emitting a Markdown heading: a model-leaked "- # heading" (or "### heading")
// normalizes to the bare text, so key-file extraction and the checkpoint region
// cannot be forged into a real section. Regression for the TrimLeft space-stop
// bug where "- # x" collapsed to "# x".
func TestAnchorLineStripsForgedHeading(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"### do not use tabs", "do not use tabs"},
		{"## Files and Evidence", "Files and Evidence"},
		{"- # heading leaked by the model", "heading leaked by the model"},
		{"- # tight variant", "tight variant"},
		{"-## another", "another"},
		{"# foo", "foo"},
		{"- normal bullet", "normal bullet"},
		{"keep API unchanged", "keep API unchanged"},
	}
	for _, c := range cases {
		if got := anchorLine(c.in, 240); got != c.want {
			t.Fatalf("anchorLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
