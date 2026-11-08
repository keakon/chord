package filectx

import (
	"fmt"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func filePartForTest(t *testing.T, data []byte, lines LineRange, maxBytes int) (string, BuildFilePartsResult) {
	t.Helper()
	reads := 0
	result := BuildFileRefPartsWithOptions(
		[]FileRef{{Path: "sample.txt", Lines: lines}},
		func(path string) string { return path },
		BuildFilePartsOptions{
			MaxFileBytes: maxBytes,
			ReadFile: func(string) ([]byte, error) {
				reads++
				return data, nil
			},
		},
	)
	if reads != 1 {
		t.Fatalf("reads = %d, want exactly one read for line facts", reads)
	}
	if len(result.Parts) != 1 {
		t.Fatalf("parts = %#v, want one injected file", result.Parts)
	}
	return result.Parts[0].Text, result
}

func openTagAttrs(text string) string {
	attrs, _, ok := strings.Cut(text, ">")
	if !ok {
		return ""
	}
	return attrs
}

func wantAttr(t *testing.T, text, name, value string) {
	t.Helper()
	want := fmt.Sprintf(" %s=%q", name, value)
	if !strings.Contains(openTagAttrs(text), want) {
		t.Fatalf("missing %q in %q", want, openTagAttrs(text))
	}
}

func noAttr(t *testing.T, text, name string) {
	t.Helper()
	if strings.Contains(openTagAttrs(text), " "+name+"=") {
		t.Fatalf("unexpected %s attribute in %q", name, openTagAttrs(text))
	}
}

func TestFileLineFactsMatchReadLineSemantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		data  string
		total string
		cov   string
	}{
		{"empty", "", "0", "none"},
		{"only line feed", "\n", "1", "1-1"},
		{"only CRLF", "\r\n", "1", "1-1"},
		{"only CR", "\r", "1", "1-1"},
		{"two blank lines", "\n\n", "2", "1-2"},
		{"trailing LF", "alpha\nbeta\n", "2", "1-2"},
		{"no final newline", "alpha\nbeta", "2", "1-2"},
		{"CRLF", "alpha\r\nbeta\r\n", "2", "1-2"},
		{"CR", "alpha\rbeta\r", "2", "1-2"},
		{"mixed separators", "alpha\r\nbeta\ngamma\rdelta\n", "4", "1-4"},
		{"BOM prefix", "\xEF\xBB\xBFalpha\nbeta\n", "2", "1-2"},
		{"BOM only", "\xEF\xBB\xBF", "0", "none"},
		{"BOM then empty line", "\xEF\xBB\xBF\n", "1", "1-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, result := filePartForTest(t, []byte(tc.data), LineRange{}, 4096)
			wantAttr(t, text, "total_lines", tc.total)
			wantAttr(t, text, "covered_lines", tc.cov)
			noAttr(t, text, "truncated")
			noAttr(t, text, "partial_last_line")
			ref, body, ok := message.ParseSingleFileRefContent(text)
			if !ok || ref.Lines != "" || body != tc.data {
				t.Fatalf("parsed body = %q, want %q (ok=%v ref=%+v)", body, tc.data, ok, ref)
			}
			if result.TotalBytes != len(tc.data) {
				t.Fatalf("TotalBytes = %d, want %d", result.TotalBytes, len(tc.data))
			}
			if result.TruncatedFiles != 0 {
				t.Fatalf("TruncatedFiles = %d, want 0", result.TruncatedFiles)
			}
		})
	}
}

func TestFileLineFactsForExplicitRanges(t *testing.T) {
	t.Parallel()
	data := "one\ntwo\nthree\nfour\nfive\n"

	text, result := filePartForTest(t, []byte(data), LineRange{Start: 2, End: 3}, 4096)
	wantAttr(t, text, "lines", "2-3")
	wantAttr(t, text, "covered_lines", "2-3")
	wantAttr(t, text, "total_lines", "5")
	noAttr(t, text, "truncated")
	ref, body, ok := message.ParseSingleFileRefContent(text)
	if !ok || ref.Lines != "2-3" || body != "two\nthree\n" {
		t.Fatalf("parsed = %+v / %q / %v", ref, body, ok)
	}
	if result.TotalBytes != len("two\nthree\n") {
		t.Fatalf("TotalBytes = %d, want selected body bytes only", result.TotalBytes)
	}

	// A requested end past EOF is clamped, not reported as budget truncation.
	text, _ = filePartForTest(t, []byte(data), LineRange{Start: 2, End: 100}, 4096)
	wantAttr(t, text, "lines", "2-100")
	wantAttr(t, text, "covered_lines", "2-5")
	wantAttr(t, text, "total_lines", "5")
	noAttr(t, text, "truncated")

	// A CRLF file keeps its raw bytes in an explicit range.
	crlf := "one\r\ntwo\r\nthree\r\n"
	text, _ = filePartForTest(t, []byte(crlf), LineRange{Start: 2, End: 2}, 4096)
	wantAttr(t, text, "covered_lines", "2-2")
	wantAttr(t, text, "total_lines", "3")
	ref, body, ok = message.ParseSingleFileRefContent(text)
	if !ok || ref.Lines != "2" || body != "two\r\n" {
		t.Fatalf("CRLF range parsed = %+v / %q / %v", ref, body, ok)
	}
}

func TestFileLineFactsSkipOutOfRangeWithoutExtraRead(t *testing.T) {
	t.Parallel()
	reads := 0
	result := BuildFileRefPartsWithOptions(
		[]FileRef{{Path: "sample.txt", Lines: LineRange{Start: 6, End: 9}}},
		func(path string) string { return path },
		BuildFilePartsOptions{
			MaxFileBytes: 4096,
			ReadFile: func(string) ([]byte, error) {
				reads++
				return []byte("one\ntwo\nthree\n"), nil
			},
		},
	)
	if len(result.Parts) != 0 || result.LoadedFiles != 0 {
		t.Fatalf("out-of-range reference produced parts: %+v", result)
	}
	if reads != 1 {
		t.Fatalf("reads = %d, want 1", reads)
	}
}

func TestFileLineFactsForCompleteLineBudgetTruncation(t *testing.T) {
	t.Parallel()
	var all strings.Builder
	for i := range 120 {
		fmt.Fprintf(&all, "line %03d\n", i+1)
	}
	data := all.String()
	first80 := strings.SplitAfter(data, "\n")[:80]
	budget := len(strings.Join(first80, ""))

	text, result := filePartForTest(t, []byte(data), LineRange{}, budget)
	wantAttr(t, text, "covered_lines", "1-80")
	wantAttr(t, text, "total_lines", "120")
	wantAttr(t, text, "truncated", "true")
	noAttr(t, text, "partial_last_line")
	if !strings.Contains(text, "file body is incomplete") {
		t.Fatalf("missing incompleteness note: %q", text)
	}
	if !strings.Contains(text, strings.Join(first80, "")) {
		t.Fatalf("retained body does not contain the first 80 complete lines: %q", text)
	}
	if strings.Contains(text, "line 081") {
		t.Fatalf("retained body crossed the covered range: %q", text)
	}
	if result.TotalBytes != budget {
		t.Fatalf("TotalBytes = %d, want %d", result.TotalBytes, budget)
	}
}

func TestFileLineFactsForUTF8SafePrefix(t *testing.T) {
	t.Parallel()
	data := strings.Repeat("é", 40) + "\nsecond\n"
	// Two 2-byte runes fit, the third would be cut in half.
	text, result := filePartForTest(t, []byte(data), LineRange{}, 5)
	wantAttr(t, text, "covered_lines", "1-1")
	wantAttr(t, text, "total_lines", "2")
	wantAttr(t, text, "truncated", "true")
	wantAttr(t, text, "partial_last_line", "true")
	if !strings.Contains(text, "éé\n[...truncated") {
		t.Fatalf("UTF-8 boundary was split or hint missing: %q", text)
	}
	if result.TotalBytes != 4 {
		t.Fatalf("TotalBytes = %d, want 4", result.TotalBytes)
	}

	// A budget too small for one rune returns an empty prefix without
	// inventing a covered line.
	text, result = filePartForTest(t, []byte(data), LineRange{}, 1)
	wantAttr(t, text, "covered_lines", "none")
	wantAttr(t, text, "total_lines", "2")
	wantAttr(t, text, "truncated", "true")
	noAttr(t, text, "partial_last_line")
	if result.TotalBytes != 0 {
		t.Fatalf("TotalBytes = %d, want 0", result.TotalBytes)
	}
}

func TestFileLineFactsDoNotSplitCRLFSeparator(t *testing.T) {
	t.Parallel()
	data := "abc\r\nsecond\r\n"
	// Four bytes would keep "abc\r" and split the separator; the CR is dropped.
	text, result := filePartForTest(t, []byte(data), LineRange{}, 4)
	wantAttr(t, text, "covered_lines", "1-1")
	wantAttr(t, text, "total_lines", "2")
	wantAttr(t, text, "truncated", "true")
	noAttr(t, text, "partial_last_line")
	if !strings.Contains(text, "\nabc\n[...truncated") || strings.Contains(text, "abc\r") {
		t.Fatalf("CRLF separator was split: %q", text)
	}
	if result.TotalBytes != 3 {
		t.Fatalf("TotalBytes = %d, want 3", result.TotalBytes)
	}
}

func TestFileLineFactsOmittedForUnverifiableEncoding(t *testing.T) {
	t.Parallel()
	data := []byte("alpha\nbeta\xFF\ngamma\n")
	text, result := filePartForTest(t, data, LineRange{}, 9)
	for _, name := range []string{"covered_lines", "total_lines", "truncated", "partial_last_line"} {
		noAttr(t, text, name)
	}
	if !strings.Contains(text, "truncated") || !strings.Contains(text, "incomplete") {
		t.Fatalf("missing incompleteness hint: %q", text)
	}
	if result.TruncatedFiles != 1 || result.TotalBytes > 9 {
		t.Fatalf("byte budget changed: %+v", result)
	}

	// UTF-16-like data is valid UTF-8 at the byte level but is decoded by read,
	// so it must not carry line claims either.
	text, _ = filePartForTest(t, []byte("a\x00b\x00\n"), LineRange{}, 4096)
	noAttr(t, text, "covered_lines")
	noAttr(t, text, "total_lines")
}

func TestFileLineFactsKeepBodyBudgetAndOmissionCount(t *testing.T) {
	t.Parallel()
	paths := []string{"a.txt", "b.txt", "c.txt"}
	reads := 0
	result := BuildFilePartsWithOptions(paths, func(path string) string { return path }, BuildFilePartsOptions{
		MaxFileBytes:  16,
		MaxTotalBytes: 32,
		ReadFile: func(path string) ([]byte, error) {
			reads++
			return []byte(strings.Repeat(path[:1], 20) + "\n"), nil
		},
	})
	// Files omitted by the total budget are never read just to count them.
	if reads != 2 {
		t.Fatalf("reads = %d, want one per loaded file only", reads)
	}
	if result.TotalBytes > 32 || result.LoadedFiles != 2 || result.OmittedFiles != 1 {
		t.Fatalf("body budget changed: %+v", result)
	}
	if len(result.Parts) != 3 {
		t.Fatalf("parts = %d, want 2 files + omission note", len(result.Parts))
	}
	note := result.Parts[2].Text
	if !strings.Contains(note, "1 additional file omitted") || note != OmittedFilesNote(1, 32) {
		t.Fatalf("omission note = %q", note)
	}
	for _, part := range result.Parts[:2] {
		wantAttr(t, part.Text, "total_lines", "1")
		wantAttr(t, part.Text, "truncated", "true")
	}
}

func TestFileLineFactsScanFullFileForLateRange(t *testing.T) {
	t.Parallel()
	data := []byte("\xEF\xBB\xBFalpha\r\n\rbravo\ncharlie")
	text, _ := filePartForTest(t, data, LineRange{Start: 2, End: 3}, 1)
	wantAttr(t, text, "lines", "2-3")
	wantAttr(t, text, "total_lines", "4")
	wantAttr(t, text, "covered_lines", "2-2")
	wantAttr(t, text, "truncated", "true")
	noAttr(t, text, "partial_last_line")
	_, body, ok := message.ParseSingleFileRefContent(text)
	if !ok || !strings.HasPrefix(body, "\r\n[...truncated") {
		t.Fatalf("late range lost the raw separator: %q", body)
	}
}
