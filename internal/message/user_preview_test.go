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
	if !IsFileRefContent("  <file path=\"p\">\n</file>") {
		t.Fatal("expected true with leading whitespace")
	}
	if IsFileRefContent("plain") {
		t.Fatal("expected false")
	}
}

func TestFirstFileRefPath(t *testing.T) {
	got, ok := FirstFileRefPath(`  <file path="dir/has\"quote&amp;space.txt">` + "\nbody\n</file>")
	if !ok {
		t.Fatal("expected file ref")
	}
	if want := `dir/has"quote&space.txt`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFileRefPaths(t *testing.T) {
	text := `<file path="a.txt">` + "\nA\n</file>" +
		`<file path='b.txt'>` + "\nB\n</file>" +
		`<file path="a.txt">` + "\nA2\n</file>" +
		`<file path="dir/has\"quote.txt">` + "\nQ\n</file>"
	got := FileRefPaths(text)
	want := []string{"a.txt", "b.txt", `dir/has"quote.txt`}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
