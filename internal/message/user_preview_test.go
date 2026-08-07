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

func TestIsUserAuthoredExcludesSyntheticUserRoleMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want bool
	}{
		{name: "ordinary user", msg: Message{Role: RoleUser, Content: "request"}, want: true},
		{name: "assistant", msg: Message{Role: RoleAssistant, Content: "reply"}},
		{name: "compaction", msg: Message{Role: RoleUser, Content: "summary", IsCompactionSummary: true}},
		{name: "mailbox", msg: Message{Role: RoleUser, Content: "mailbox", Kind: KindSubAgentMailbox}},
		{name: "loop notice", msg: Message{Role: RoleUser, Content: "loop", Kind: KindLoopNotice}},
		{name: "background result", msg: Message{Role: RoleUser, Content: "job finished", Kind: KindBackgroundResult}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUserAuthored(tt.msg); got != tt.want {
				t.Fatalf("IsUserAuthored() = %v, want %v", got, tt.want)
			}
		})
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

func TestFirstFileRefPathAllowsAdditionalAttributes(t *testing.T) {
	got, ok := FirstFileRefPath(`<file path="a.txt" lines="2-3">` + "\nbody\n</file>")
	if !ok {
		t.Fatal("expected file ref")
	}
	if got != "a.txt" {
		t.Fatalf("got %q, want a.txt", got)
	}
}

func TestParseSingleFileRefContentPreservesExactBody(t *testing.T) {
	ref, body, ok := ParseSingleFileRefContent(`<file path="a.txt">` + "\nfirst\nsecond\n\n</file>\n")
	if !ok {
		t.Fatal("ParseSingleFileRefContent returned false")
	}
	if ref.Path != "a.txt" || ref.Lines != "" {
		t.Fatalf("ref = %#v", ref)
	}
	if body != "first\nsecond\n" {
		t.Fatalf("body = %q, want exact file bytes", body)
	}
}

func TestParseSingleFileRefContentReturnsLineRange(t *testing.T) {
	ref, body, ok := ParseSingleFileRefContent(`<file path="a.txt" lines="2-3">` + "\ntwo\nthree\n</file>")
	if !ok || ref.Path != "a.txt" || ref.Lines != "2-3" || body != "two\nthree" {
		t.Fatalf("ParseSingleFileRefContent = (%#v, %q, %v)", ref, body, ok)
	}
}

func TestParseSingleFileRefContentRejectsSurroundingText(t *testing.T) {
	if _, _, ok := ParseSingleFileRefContent("prefix\n<file path=\"a.txt\">\nbody\n</file>"); ok {
		t.Fatal("ParseSingleFileRefContent accepted surrounding text")
	}
}

func TestFileRefsIncludesLineMetadata(t *testing.T) {
	got := FileRefs(`<file path="a.txt" lines="2-3">` + "\nbody\n</file>" + `<file path='b.txt'>` + "\nB\n</file>")
	want := []FileRef{{Path: "a.txt", Lines: "2-3"}, {Path: "b.txt"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
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
