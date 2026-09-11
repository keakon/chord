package tools

import (
	"testing"
)

func TestCleanJobOutputText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello\n", "hello\n"},
		{"sgr color", "\x1b[31merror\x1b[0m\n", "error\n"},
		{"cursor sequence", "\x1b[2Kdone", "done"},
		{"progress redraw", "10%\r50%\r100%\n", "100%\n"},
		{"progress across lines", "a\rb\nc\rd\n", "b\nd\n"},
		{"crlf line endings", "line\r\nnext\r\n", "line\nnext\n"},
		{"trailing carriage return keeps the text", "done\r", "done"},
		{"leading carriage return on a line", "first\n\rlast", "first\nlast"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanJobOutputText(tt.in); got != tt.want {
				t.Fatalf("cleanJobOutputText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
