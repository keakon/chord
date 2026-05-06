package terminaltitle

import "testing"

func TestComposeTitle_PrefixesAndSanitizes(t *testing.T) {
	if got := ComposeTitle("my task", "⠼"); got != "⠼ my task" {
		t.Fatalf("ComposeTitle spinner = %q, want %q", got, "⠼ my task")
	}
	if got := ComposeTitle("my task", " "); got != "  my task" {
		t.Fatalf("ComposeTitle placeholder = %q, want %q", got, "  my task")
	}
	if got := ComposeTitle("hello\nworld", "x\n"); got != "x hello world" {
		t.Fatalf("ComposeTitle sanitization = %q, want %q", got, "x hello world")
	}
	if got := ComposeTitle("\x1b\n\r\t", "x"); got != "" {
		t.Fatalf("ComposeTitle empty title = %q, want empty", got)
	}
}
