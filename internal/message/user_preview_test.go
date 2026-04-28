package message

import "testing"

func TestUserPromptPlainText_PrefersNonFileParts(t *testing.T) {
	msg := Message{
		Role: "user",
		Parts: []ContentPart{
			{Type: "text", Text: "user prompt"},
			{Type: "text", Text: `<file path="a.txt">` + "\nbody\n" + `</file>`},
		},
		Content: "ignored when parts set",
	}
	if got := UserPromptPlainText(msg); got != "user prompt" {
		t.Fatalf("got %q", got)
	}
}

func TestUserPromptPlainText_ContentReturnsTrimmedRawContent(t *testing.T) {
	msg := Message{
		Role:    "user",
		Content: `<file path="x">` + "\nZ\n" + `</file>` + "\nuser asks",
	}
	want := `<file path="x">` + "\nZ\n" + `</file>` + "\nuser asks"
	if got := UserPromptPlainText(msg); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestIsFileRefContent(t *testing.T) {
	if !IsFileRefContent("<file path=\"p\">\n</file>") {
		t.Fatal("expected true")
	}
	if IsFileRefContent("plain") {
		t.Fatal("expected false")
	}
}
