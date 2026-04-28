package terminaltitle

import (
	"bytes"
	"strings"
	"testing"
)

func extractTitleFromOSC(t *testing.T, out string) string {
	t.Helper()
	if !strings.HasPrefix(out, "\x1b]0;") || !strings.HasSuffix(out, "\x1b\\") {
		t.Fatalf("output %q is not a valid OSC title", out)
	}
	return out[len("\x1b]0;") : len(out)-len("\x1b\\")]
}

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

func TestSetWindowTitle_WritesOSCSequence(t *testing.T) {
	var buf bytes.Buffer
	err := SetWindowTitle(&buf, "test title")
	if err != nil {
		t.Fatalf("SetWindowTitle: %v", err)
	}
	out := buf.String()
	// Should start with OSC 0 and end with ST
	if !strings.HasPrefix(out, "\x1b]0;") {
		t.Errorf("output %q does not start with OSC 0 prefix", out)
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Errorf("output %q does not end with ST suffix", out)
	}
	// Title should be in between
	title := out[len("\x1b]0;") : len(out)-len("\x1b\\")]
	if title != "test title" {
		t.Errorf("title in output = %q, want %q", title, "test title")
	}
}

func TestSetWindowTitleWithSpinner_PrefixesSpinner(t *testing.T) {
	var buf bytes.Buffer
	err := SetWindowTitleWithSpinner(&buf, "my task", "⠼")
	if err != nil {
		t.Fatalf("SetWindowTitleWithSpinner: %v", err)
	}
	got := extractTitleFromOSC(t, buf.String())
	want := "⠼ my task"
	if got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

func TestSetWindowTitleWithPrefix_AllowsFixedWidthPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	err := SetWindowTitleWithPrefix(&buf, "my task", " ")
	if err != nil {
		t.Fatalf("SetWindowTitleWithPrefix: %v", err)
	}
	got := extractTitleFromOSC(t, buf.String())
	want := "  my task"
	if got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

func TestSetWindowTitle_EmptyAfterSanitization(t *testing.T) {
	var buf bytes.Buffer
	// Pure control characters should result in empty title and no write
	err := SetWindowTitle(&buf, "\x1b\n\r\t")
	if err != nil {
		t.Fatalf("SetWindowTitle: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no output for invisible title, got %q", buf.String())
	}
}

func TestSpinnerFrames_Cycles(t *testing.T) {
	ResetSpinner()
	// Cycle through twice to verify ordering
	for i := 0; i < len(SpinnerFrames)*2; i++ {
		frame := NextSpinnerFrame()
		expected := SpinnerFrames[i%len(SpinnerFrames)]
		if frame != expected {
			t.Errorf("frame %d = %q, want %q", i, frame, expected)
		}
	}
}

func TestDefaultTitle(t *testing.T) {
	if DefaultTitle != "chord" {
		t.Errorf("DefaultTitle = %q, want %q", DefaultTitle, "chord")
	}
}
