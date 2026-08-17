package message

import "testing"

func TestHasVisibleText(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "empty", text: "", want: false},
		{name: "whitespace", text: " \n\t", want: false},
		{name: "control", text: "\x00\x1f", want: false},
		{name: "zero width space", text: "\u200b\u200b", want: false},
		{name: "byte order mark", text: "\ufeff", want: false},
		{name: "zero width joiner", text: "\u200d", want: false},
		{name: "combining mark", text: "\u0301", want: false},
		{name: "variation selector", text: "\ufe0f", want: false},
		{name: "plain text", text: "answer", want: true},
		{name: "visible text with format prefix", text: "\u200banswer", want: true},
		{name: "emoji ZWJ sequence", text: "👩‍💻", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasVisibleText(tt.text); got != tt.want {
				t.Fatalf("HasVisibleText(%q) = %t, want %t", tt.text, got, tt.want)
			}
		})
	}
}

func TestNormalizeInvisibleTextPreservesVisibleUnicodeSequence(t *testing.T) {
	const visible = "\u200b👩‍💻"
	if got := NormalizeInvisibleText(visible); got != visible {
		t.Fatalf("NormalizeInvisibleText(%q) = %q, want unchanged", visible, got)
	}
	if got := NormalizeInvisibleText("\u200b\u200b"); got != "" {
		t.Fatalf("NormalizeInvisibleText(zero-width spaces) = %q, want empty", got)
	}
}
