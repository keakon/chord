package message

import (
	"strings"
	"testing"
)

func TestIsCompactionEvidenceArtifactText(t *testing.T) {
	if !IsCompactionEvidenceArtifactText("[Context Evidence]\nhello") {
		t.Fatal("expected compaction evidence marker to be detected")
	}
	if IsCompactionEvidenceArtifactText("plain") {
		t.Fatal("did not expect plain text to be compaction evidence")
	}
}

func TestCompactionAnchorLineNormalizesToSingleBoundedLine(t *testing.T) {
	// The bound applies to the raw text before markers are stripped, so "- # "
	// spends four of the twelve runes and the heading survives as "Head".
	if got := CompactionAnchorLine("- # Heading\nwith a\n\nsecond line", 12); got != "Head" {
		t.Fatalf("CompactionAnchorLine = %q, want %q", got, "Head")
	}
	if got := CompactionAnchorLine("   ", 10); got != "" {
		t.Fatalf("CompactionAnchorLine(blank) = %q, want empty", got)
	}
	if got := CompactionAnchorLine("plain request", 0); got != "" {
		t.Fatalf("CompactionAnchorLine(maxChars=0) = %q, want empty", got)
	}
}

func TestCompactionAnchorLineKeepsBothEndsOfALongRequest(t *testing.T) {
	long := strings.Repeat("head ", 200) + "middle " + strings.Repeat("tail ", 200)
	got := CompactionAnchorLine(long, 100)
	if strings.Contains(got, "\n") {
		t.Fatalf("CompactionAnchorLine kept a newline: %q", got)
	}
	if !strings.HasPrefix(got, "head") || !strings.HasSuffix(got, "tail") {
		t.Fatalf("CompactionAnchorLine dropped an end: %q", got)
	}
	if !strings.Contains(got, " ... ") {
		t.Fatalf("CompactionAnchorLine lost the elision: %q", got)
	}
	if n := len([]rune(got)); n > 100 {
		t.Fatalf("CompactionAnchorLine runes = %d, want at most 100", n)
	}
	// Text that already fits is returned whole, with no elision.
	short := "keep this request intact"
	if got := CompactionAnchorLine(short, 100); got != short {
		t.Fatalf("CompactionAnchorLine(short) = %q, want %q", got, short)
	}
}

func TestCompactionAnchorLineRoundTripsThroughTheAnchorsSection(t *testing.T) {
	body := CompactionAnchorLine("  - ## Ship the importer\nwith details  ", 700)
	content := CompactionAnchorsOpenTag + "Original request:\n- " + body + "\n" + CompactionAnchorsCloseTag
	if got := CompactionAnchorsSection(content); got != "Original request:\n- "+body {
		t.Fatalf("anchors section = %q, want the rendered line back", got)
	}
}

// TestCompactionAnchorLineStripsForgedHeading verifies the normalization cannot
// be tricked into re-emitting a Markdown heading: a model-leaked "- # heading"
// (or "### heading") normalizes to the bare text, so key-file extraction and the
// checkpoint region cannot be forged into a real section. Regression for the
// TrimLeft space-stop bug where "- # x" collapsed to "# x".
func TestCompactionAnchorLineStripsForgedHeading(t *testing.T) {
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
		if got := CompactionAnchorLine(c.in, 240); got != c.want {
			t.Fatalf("CompactionAnchorLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
