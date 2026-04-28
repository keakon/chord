package tui

import (
	"testing"

	"github.com/keakon/chord/internal/terminaltitle"
)

func TestDeriveTerminalTitle_PlainText(t *testing.T) {
	got := deriveTerminalTitle("帮我重构这个模块")
	want := "帮我重构这个模块"
	if got != want {
		t.Errorf("deriveTerminalTitle = %q, want %q", got, want)
	}
}

func TestDeriveTerminalTitle_CollapsesWhitespace(t *testing.T) {
	got := deriveTerminalTitle("  hello   \n  world  \t test  ")
	want := "hello world test"
	if got != want {
		t.Errorf("deriveTerminalTitle = %q, want %q", got, want)
	}
}

func TestDeriveTerminalTitle_TruncatesLongText(t *testing.T) {
	long := "这是一条非常非常非常非常非常非常非常非常非常非常非常非常长的消息"
	got := deriveTerminalTitle(long)
	if len([]rune(got)) > terminaltitle.MaxTitleRunes+1 { // +1 for ellipsis
		t.Errorf("deriveTerminalTitle produced %d runes, want <= %d", len([]rune(got)), terminaltitle.MaxTitleRunes+1)
	}
	// Should end with ellipsis
	runes := []rune(got)
	if runes[len(runes)-1] != '…' {
		t.Errorf("deriveTerminalTitle(%q) = %q, should end with ellipsis", long, got)
	}
}

func TestDeriveTerminalTitle_EmptyReturnsDefault(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", terminaltitle.DefaultTitle},
		{"   ", terminaltitle.DefaultTitle},
		{"\n\t", terminaltitle.DefaultTitle},
		{"\x1b[0m", "[0m"}, // ESC removed, but [0m are printable chars
	}
	for _, tt := range tests {
		got := deriveTerminalTitle(tt.input)
		if got != tt.want {
			t.Errorf("deriveTerminalTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDeriveTerminalTitle_RemovesNewlines(t *testing.T) {
	got := deriveTerminalTitle("first line\nsecond line\nthird line")
	// After collapsing, it may be truncated
	if len([]rune(got)) > terminaltitle.MaxTitleRunes {
		got = string([]rune(got)[:terminaltitle.MaxTitleRunes])
	}
	// First part should match
	expected := "first line second li"
	if len([]rune(got)) >= len([]rune(expected)) {
		prefix := string([]rune(got)[:len([]rune(expected))])
		if prefix != expected {
			t.Errorf("deriveTerminalTitle = %q, prefix should be %q", got, expected)
		}
	}
}
