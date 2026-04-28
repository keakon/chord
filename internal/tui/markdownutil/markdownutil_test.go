package markdownutil

import "testing"

func TestNormalizeNewlines(t *testing.T) {
	got := NormalizeNewlines("a\r\nb\rc")
	if got != "a\nb\nc" {
		t.Fatalf("NormalizeNewlines = %q", got)
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
