package markdownutil

import (
	"strings"
	"testing"
)

func TestNormalizeNewlines(t *testing.T) {
	got := NormalizeNewlines("a\r\nb\rc")
	if got != "a\nb\nc" {
		t.Fatalf("NormalizeNewlines = %q, want %q", got, "a\nb\nc")
	}
}

func TestFirstFenceInfoField(t *testing.T) {
	tests := []struct{ in, want string }{
		{in: "go title=example", want: "go"},
		{in: "  python  ", want: "python"},
		{in: "   ", want: ""},
	}
	for _, tc := range tests {
		if got := FirstFenceInfoField(tc.in); got != tc.want {
			t.Fatalf("FirstFenceInfoField(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseFenceLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Fence
		ok   bool
	}{
		{name: "backtick opener", line: "  ```go title=x\n", want: Fence{Indent: "  ", Delimiter: '`', Length: 3, Info: "go title=x"}, ok: true},
		{name: "tilde opener", line: "~~~~ text", want: Fence{Delimiter: '~', Length: 4, Info: "text"}, ok: true},
		{name: "too short", line: "``", ok: false},
		{name: "backtick info cannot contain backtick", line: "```go`bad", ok: false},
		{name: "blank", line: "   ", ok: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseFenceLine(tc.line)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("ParseFenceLine(%q) = (%+v, %v), want (%+v, %v)", tc.line, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestIsFenceClose(t *testing.T) {
	open := Fence{Indent: "", Delimiter: '`', Length: 3, Info: "go"}
	tests := []struct {
		line string
		want bool
	}{
		{line: "```", want: true},
		{line: "````", want: true},
		{line: "``", want: false},
		{line: "~~~", want: false},
		{line: "``` go", want: false},
	}
	for _, tc := range tests {
		if got := IsFenceClose(tc.line, open); got != tc.want {
			t.Fatalf("IsFenceClose(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
}

func TestFindStreamingSettledFrontier(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
	}{
		{name: "empty", content: "", want: 0},
		{name: "blank line settles previous paragraph", content: "para\n\nstreaming", want: len("para\n")},
		{name: "closed fence settles through close", content: "```go\nfmt.Println(1)\n```\nnext", want: len("```go\nfmt.Println(1)\n```\n")},
		{name: "open fence unsettled", content: "intro\n```go\nfmt.", want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindStreamingSettledFrontier(tc.content); got != tc.want {
				t.Fatalf("FindStreamingSettledFrontier() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestStreamingFrontierScannerMatchesFind(t *testing.T) {
	// Verify that StreamingFrontierScanner.Advance (incremental) produces
	// the same results as FindStreamingSettledFrontier (full-scan).
	// Test full-content scan first, then incremental appends.
	parts := []string{
		"",
		"para one continues\n",
		"still no blank\n\n",
		"settled paragraph\n",
		"```go\nfunc main() {\n",
		"\tfmt.Println(\"hi\")\n",
		"```\n",
		"after fence\n",
	}
	var full string
	var s StreamingFrontierScanner
	for _, part := range parts {
		full += part
		want := FindStreamingSettledFrontier(full)
		got := s.Advance(full)
		if got != want {
			t.Fatalf("after append %q: Advance() = %d, want %d (full=%q)", part, got, want, full)
		}
	}
}

func TestStreamingFrontierScannerReplaysUnterminatedLine(t *testing.T) {
	// Appends frequently land mid-line; the scanner must rescan the last
	// unterminated line so incremental results stay identical to full scans.
	cases := []struct {
		name  string
		parts []string
	}{
		{"paragraph then blank line", []string{"hello", "\n\nworld"}},
		{"partial blank line grows into text", []string{"a\n", " ", "x\n\ndone\n"}},
		{"fence opener split mid-line", []string{"``", "`go\nbody\n", "```\nafter\n\nnext"}},
		{"crlf split across appends", []string{"para\r", "\n\r\nnext\r\n"}},
		{"repeated advance on unterminated tail", []string{"hello", "", "\n\nworld", ""}},
		{"single line grows then terminates", []string{"abc", "def", "ghi\nnext\n\ntail"}},
		{"fence opener invalidated by later backtick", []string{"```", "a`b\n\nrest\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s StreamingFrontierScanner
			var full string
			for _, part := range tc.parts {
				full += part
				want := FindStreamingSettledFrontier(full)
				if got := s.Advance(full); got != want {
					t.Fatalf("after append %q: Advance() = %d, want %d (full=%q)", part, got, want, full)
				}
			}
		})
	}
}

func TestStreamingFrontierScannerResetsOnNonAppend(t *testing.T) {
	s := StreamingFrontierScanner{}
	s.Advance("hello\n\nworld")
	// Shrink content – should reset.
	got := s.Advance("hello\n\n")
	want := FindStreamingSettledFrontier("hello\n\n")
	if got != want {
		t.Fatalf("after shrink: Advance() = %d, want %d", got, want)
	}
}

func TestRepairForDisplayClosesOpenFence(t *testing.T) {
	tests := []struct {
		name, content, want string
	}{
		{name: "backtick", content: "```go\nfmt.Println(1)", want: "```go\nfmt.Println(1)\n```"},
		{name: "preserves indent and tilde length", content: "  ~~~~text\nbody\n", want: "  ~~~~text\nbody\n  ~~~~"},
		{name: "closed unchanged", content: "```\nok\n```\n", want: "```\nok\n```\n"},
		{name: "normalizes crlf", content: "```\r\nok", want: "```\nok\n```"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RepairForDisplay(tc.content); got != tc.want {
				t.Fatalf("RepairForDisplay() = %q, want %q", got, tc.want)
			}
		})
	}
}

func buildStreamingFrontierBenchmarkContent() string {
	var b strings.Builder
	for i := range 1024 {
		b.WriteString("streaming paragraph line that should stay cheap until a blank line arrives\n")
		if i%8 == 7 {
			b.WriteByte('\n')
		}
	}
	b.WriteString("tail without a structural boundary")
	return b.String()
}

func buildStreamingFrontierBenchmarkSnapshots() []string {
	const chunks = 1024
	snapshots := make([]string, 0, chunks+1)
	var b strings.Builder
	for i := range chunks {
		b.WriteString("streaming paragraph line that should stay cheap until a blank line arrives\n")
		if i%8 == 7 {
			b.WriteByte('\n')
		}
		snapshots = append(snapshots, b.String())
	}
	b.WriteString("tail without a structural boundary")
	snapshots = append(snapshots, b.String())
	return snapshots
}

func BenchmarkFindStreamingSettledFrontierLongContent(b *testing.B) {
	content := buildStreamingFrontierBenchmarkContent()
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	for b.Loop() {
		_ = FindStreamingSettledFrontier(content)
	}
}

func BenchmarkFindStreamingSettledFrontierAppendSnapshots(b *testing.B) {
	snapshots := buildStreamingFrontierBenchmarkSnapshots()
	b.SetBytes(int64(len(snapshots[len(snapshots)-1])))
	b.ReportAllocs()
	for b.Loop() {
		for _, content := range snapshots {
			_ = FindStreamingSettledFrontier(content)
		}
	}
}

func BenchmarkStreamingFrontierScannerAppendSnapshots(b *testing.B) {
	snapshots := buildStreamingFrontierBenchmarkSnapshots()
	b.SetBytes(int64(len(snapshots[len(snapshots)-1])))
	b.ReportAllocs()
	for b.Loop() {
		var scanner StreamingFrontierScanner
		for _, content := range snapshots {
			_ = scanner.Advance(content)
		}
	}
}

func TestFindStreamingSettledFrontierLongContentAllocsGuard(t *testing.T) {
	content := buildStreamingFrontierBenchmarkContent()
	allocs := testing.AllocsPerRun(50, func() {
		_ = FindStreamingSettledFrontier(content)
	})
	if allocs > 0 {
		t.Fatalf("FindStreamingSettledFrontier allocs = %.0f, want 0", allocs)
	}
}
