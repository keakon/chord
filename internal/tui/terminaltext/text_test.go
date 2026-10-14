package terminaltext

import (
	"strings"
	"testing"
)

func TestExpandTabsForDisplayUsesFixedTabWidth(t *testing.T) {
	got := ExpandTabs(" \tswitch", TabWidth)
	want := "    switch"
	if got != want {
		t.Fatalf("ExpandTabs() = %q, want %q", got, want)
	}
}

func TestExpandTabsForDisplayANSIIgnoresEscapeSequenceWidth(t *testing.T) {
	input := "\x1b[48;5;235mA\tB\x1b[m"
	got := ExpandTabsANSI(input, 4)
	// Visible text should align as if the escape sequence has zero width.
	if StripANSI(got) != "A   B" {
		t.Fatalf("StripANSI(ExpandTabsANSI())=%q, want %q", StripANSI(got), "A   B")
	}
	if strings.ContainsRune(got, '\t') {
		t.Fatalf("ExpandTabsANSI should remove tabs, got %q", got)
	}
	// Preserve the SGR sequences.
	if !strings.Contains(got, "\x1b[48;5;235m") || !strings.Contains(got, "\x1b[m") {
		t.Fatalf("expected output to preserve ANSI sequences, got %q", got)
	}
}

func TestWrapPreformattedTextPreservesIndentationAndTabs(t *testing.T) {
	lines := WrapPreformatted("-\t\tif llm.IsResponsesModel(modelID) || normalizedCfg.Preset == \"codex\" {", 30)
	if len(lines) < 2 {
		t.Fatalf("WrapPreformatted() should soft-wrap long preformatted line, got %#v", lines)
	}
	if !strings.HasPrefix(lines[0], "-       if ") {
		t.Fatalf("first wrapped line should preserve expanded tab indentation, got %q", lines[0])
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "IsResponsesModel(modelID)") {
		t.Fatalf("wrapped text lost code content: %#v", lines)
	}
}

func TestSanitizeDisplayTextEscapesRawControlCharacters(t *testing.T) {
	var input strings.Builder
	input.WriteString("start\tkeep\n")
	for c := range byte(0x20) {
		if c == '\t' || c == '\n' {
			continue
		}
		input.WriteByte(c)
	}
	input.WriteByte(0x7f)
	input.WriteString("end")

	got := Sanitize(input.String())
	if strings.ContainsRune(got, '\x00') {
		t.Fatalf("sanitized text still contains NUL: %q", got)
	}
	for _, r := range got {
		if (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f {
			t.Fatalf("sanitized text leaked raw control character %#U in %q", r, got)
		}
	}
	for _, want := range []string{`\x00`, `\x01`, `\x1b`, `\x1f`, `\x7f`, `\a`, `\b`, `\f`, `\v`} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized text missing %s in %q", want, got)
		}
	}
	if !strings.Contains(got, "start\tkeep\n") {
		t.Fatalf("sanitized text should preserve tab and newline, got %q", got)
	}
}

func TestSanitizeDisplayTextEscapesC1ControlCharacters(t *testing.T) {
	input := "raw " + string([]byte{0x9b}) + " and utf8 " + string(rune(0x9b))
	got := Sanitize(input)
	if strings.ContainsRune(got, rune(0x9b)) {
		t.Fatalf("sanitized text leaked C1 control character: %q", got)
	}
	if strings.Count(got, `\x9b`) != 2 {
		t.Fatalf("sanitized text should contain two escaped C1 controls, got %q", got)
	}
}

func TestWrapPreformattedTextUsesGraphemeClustersForEmoji(t *testing.T) {
	line := "👩‍⚕️x"
	if got, want := Width(line), 3; got != want {
		t.Fatalf("test fixture width=%d want %d", got, want)
	}
	lines := WrapPreformatted(line, 3)
	if len(lines) != 1 || lines[0] != line {
		t.Fatalf("WrapPreformatted split grapheme cluster line: %#v", lines)
	}
}

func TestPadLineToDisplayWidthUsesViewportGraphemeWidth(t *testing.T) {
	line := "👩‍⚕️x"
	padded := Pad(line, 3)
	if got := StripANSI(padded); got != line {
		t.Fatalf("Pad added padding with viewport-full grapheme line: %q", got)
	}
}

func TestTruncateLineToDisplayWidthKeepsEmojiGraphemeIntact(t *testing.T) {
	line := "⚠️ ok"
	got := Truncate(line, 2)
	if got != "⚠️" {
		t.Fatalf("Truncate split or overran emoji grapheme: %q", got)
	}
}

func TestWrapPreformattedTextKeepsOversizeGraphemeWithoutLeadingBlank(t *testing.T) {
	lines := WrapPreformatted("⚠️x", 1)
	want := []string{"⚠️", "x"}
	if len(lines) != len(want) {
		t.Fatalf("WrapPreformatted lines=%#v want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("WrapPreformatted lines=%#v want %#v", lines, want)
		}
	}
}

func TestWrapTextKeepsOversizeGraphemeWithoutLeadingBlank(t *testing.T) {
	lines := Wrap("⚠️x", 1)
	want := []string{"⚠️", "x"}
	if len(lines) != len(want) {
		t.Fatalf("Wrap lines=%#v want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("Wrap lines=%#v want %#v", lines, want)
		}
	}
}

func TestWrapTextLongWordsAccountForIndent(t *testing.T) {
	for _, text := range []string{
		"  " + strings.Repeat("x", 23),
		"  " + strings.Repeat("界", 12),
	} {
		lines := Wrap(text, 24)
		for _, line := range lines {
			if got := Width(line); got > 24 {
				t.Fatalf("Wrap(%q) line width = %d, want <= 24: %q", text, got, line)
			}
		}
	}
}

func BenchmarkWrapTextLongParagraph(b *testing.B) {
	text := strings.Repeat("streaming cheap path line with no markdown fences or bullets ", 160)
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	for b.Loop() {
		_ = Wrap(text, 100)
	}
}

func TestWrapTextLongParagraphAllocsGuard(t *testing.T) {
	text := strings.Repeat("streaming cheap path line with no markdown fences or bullets ", 160)
	allocs := testing.AllocsPerRun(50, func() {
		_ = Wrap(text, 100)
	})
	if allocs > 600 {
		t.Fatalf("Wrap allocs = %.0f, want <= 600", allocs)
	}
}

func TestTruncateLineToDisplayWidthDropsOversizeFirstGraphemeStrictly(t *testing.T) {
	got := Truncate("⚠️x", 1)
	if got != "" {
		t.Fatalf("Truncate should not exceed requested width: %q", got)
	}
}

func TestTuiWrapHeadTailKeepsStyledOversizeFirstGrapheme(t *testing.T) {
	line := "\x1b[38;5;196m⚠️x\x1b[0m"
	head, tail := WrapHeadTail(line, 1)
	if got := StripANSI(head); got != "⚠️" {
		t.Fatalf("head plain=%q want ⚠️; ansi=%q", got, head)
	}
	if got := StripANSI(tail); got != "x" {
		t.Fatalf("tail plain=%q want x; ansi=%q", got, tail)
	}
}

// preserveBackgroundReference is the four-ReplaceAll implementation the
// single-pass version replaced. It is kept only as the oracle for
// TestPreserveBackgroundMatchesReference.
func preserveBackgroundReference(line, bgColor string) string {
	if bgColor == "" || line == "" {
		return line
	}
	if !strings.Contains(line, "\x1b[") {
		return line
	}
	bgSeq := BackgroundSequence(bgColor)
	if bgSeq == "" {
		return line
	}
	line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+bgSeq)
	line = strings.ReplaceAll(line, "\x1b[m", "\x1b[m"+bgSeq)
	line = strings.ReplaceAll(line, "\x1b[49m", "\x1b[49m"+bgSeq)
	line = strings.ReplaceAll(line, "\x1b[39m", "\x1b[39m"+bgSeq)
	return line
}

func TestPreserveBackgroundMatchesReference(t *testing.T) {
	bgColors := []string{"", "236", "52", "#1f2430"}
	pieces := []string{
		"", "text", "\x1b[0m", "\x1b[m", "\x1b[49m", "\x1b[39m",
		"\x1b[38;5;120m", "\x1b[1;31m", "\x1b[K", "\x1b[", "\x1b", "[0m",
		"\x1b[0", "\x1b[0mm", "\x1b[m0m", "\x1b]8;;url\x07", "\x1b[1000m",
	}
	var cases []string
	for i := range pieces {
		cases = append(cases, pieces[i])
		for j := range pieces {
			cases = append(cases, pieces[i]+pieces[j])
			for k := range pieces {
				cases = append(cases, pieces[i]+pieces[j]+pieces[k])
			}
		}
	}
	for _, line := range cases {
		for _, bg := range bgColors {
			want := preserveBackgroundReference(line, bg)
			got := PreserveBackground(line, bg)
			if got != want {
				t.Fatalf("PreserveBackground(%q,%q)\n got=%q\nwant=%q", line, bg, got, want)
			}
		}
	}
}
