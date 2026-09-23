package terminaltitle

import (
	"slices"
	"strings"
	"testing"
)

func TestSanitizeTitle_RemovesControlCharacters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "collapses whitespace",
			input: "  hello   world  ",
			want:  "hello world",
		},
		{
			name:  "removes newlines and tabs",
			input: "hello\nworld\ttest",
			want:  "hello world test",
		},
		{
			name:  "removes ESC control char but leaves printable ANSI tail",
			input: "hello\x1b[31m world",
			want:  "hello[31m world", // \x1b removed, [31m are printable chars
		},
		{
			name:  "removes bidi controls",
			input: "hello\u202Eworld",
			want:  "helloworld",
		},
		{
			name:  "leaves printable ANSI tail after ESC removal",
			input: "\x1b[0m",
			want:  "[0m", // ESC removed, rest stays
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTitle(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSanitizeTitle_TruncatesToMaxRunes(t *testing.T) {
	long := strings.Repeat("a", 50)
	got := sanitizeTitle(long)
	if len([]rune(got)) > MaxTitleRunes {
		t.Errorf("sanitizeTitle produced %d runes, want <= %d", len([]rune(got)), MaxTitleRunes)
	}
}

func TestSpinnerFrames_Cycles(t *testing.T) {
	first := NextSpinnerFrame()
	start := slices.Index(SpinnerFrames, first)
	if start < 0 {
		t.Fatalf("NextSpinnerFrame returned %q, not a known frame", first)
	}
	// Cycle through twice to verify ordering
	for i := 1; i < len(SpinnerFrames)*2; i++ {
		frame := NextSpinnerFrame()
		expected := SpinnerFrames[(start+i)%len(SpinnerFrames)]
		if frame != expected {
			t.Errorf("frame %d = %q, want %q", i, frame, expected)
		}
	}
}

func TestComposeTitle_ComposesSanitizedPrefixAndTitle(t *testing.T) {
	got := ComposeTitle("my task", "⠼")
	want := "⠼ my task"
	if got != want {
		t.Fatalf("ComposeTitle = %q, want %q", got, want)
	}
}

func TestComposeTitle_EmptyAfterSanitizationReturnsEmpty(t *testing.T) {
	if got := ComposeTitle("\x1b\n\r\t", "⠼"); got != "" {
		t.Fatalf("ComposeTitle should return empty string for invisible title, got %q", got)
	}
}
