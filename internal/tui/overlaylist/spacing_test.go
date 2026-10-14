package overlaylist

import "testing"

func TestSpacingMarksKeepTablePaddingAligned(t *testing.T) {
	const glyph = "a\u0cf3"
	if got := formatTableCell(glyph, 4, 0); got != glyph+"  " {
		t.Fatalf("padded cell = %q", got)
	}
}
